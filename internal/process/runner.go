package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"golang.org/x/sys/unix"
)

type Runner struct {
	GracePeriod time.Duration
}

func (r Runner) Run(
	ctx context.Context,
	timeout time.Duration,
	name string,
	args ...string,
) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return r.RunCommand(ctx, timeout, cmd)
}

// RunCommand applies the same bounded output and process-tree lifecycle to a
// caller-configured command, preserving its explicit environment and directory.
func (r Runner) RunCommand(ctx context.Context, timeout time.Duration, cmd *exec.Cmd) ([]byte, error) {

	var runCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if err := runCtx.Err(); err != nil {
		reason := ErrProcessCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			reason = ErrProcessTimeout
		}
		return nil, fmt.Errorf("%w: %w", reason, err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// A caller cannot join another group's lifecycle or inherit a foreign PGID.
	cmd.SysProcAttr.Pgid = 0
	var outBuf bytes.Buffer
	lw := &limitedWriter{buf: &outBuf, limit: 1 << 20}
	cmd.Stdout, cmd.Stderr = lw, lw
	// Escaped descendants can retain output pipes in monitored mode. This bounds
	// pipe draining; only the required-mode PID namespace confines all descendants.
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 500 * time.Millisecond
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProcessStart, err)
	}
	pgid := cmd.Process.Pid
	if pgid <= 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("%w: invalid process group ID %d", ErrProcessStart, pgid)
	}
	// Observe exit WITHOUT reaping the leader. Its retained kernel identity pins
	// the PGID through group cleanup, preventing signals to a recycled PID/PGID.
	// Calling Cmd.Wait concurrently here would release that safety boundary.
	exited := make(chan error, 1)
	go func() {
		var status unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, pgid, &status, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			exited <- err
			return
		}
	}()
	var reason error
	var observeErr error
	select {
	case observeErr = <-exited:
	case <-runCtx.Done():
		reason = ErrProcessCancelled
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			reason = ErrProcessTimeout
		}
		grace := r.GracePeriod
		if grace <= 0 {
			grace = 500 * time.Millisecond
		}
		_ = KillProcessGroup(pgid, grace)
		observeErr = <-exited
	}
	// The leader is still an unreaped child. Terminate same-group background
	// helpers even after a successful leader exit, before removing its identity.
	if observeErr == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	waitErr := cmd.Wait()
	if reason != nil {
		return outBuf.Bytes(), fmt.Errorf("%w: %w", reason, runCtx.Err())
	}
	if observeErr != nil {
		return outBuf.Bytes(), fmt.Errorf("%w: observe process exit: %v", ErrProcessExit, observeErr)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return outBuf.Bytes(), fmt.Errorf("%w: %w", ErrProcessCancelled, ctx.Err())
		}
		return outBuf.Bytes(), fmt.Errorf("%w: %w", ErrProcessExit, waitErr)
	}
	return outBuf.Bytes(), nil
}

// KillProcessGroup safely terminates a Linux process group.
// It sends SIGTERM to -pgid, waits for gracePeriod, and sends SIGKILL if still alive.
func KillProcessGroup(pgid int, gracePeriod time.Duration) error {
	if pgid <= 1 {
		return nil
	}

	// Check if process group exists (signal 0)
	if err := syscall.Kill(-pgid, 0); err != nil {
		// ESRCH means process group does not exist / exited
		return nil
	}

	// Send SIGTERM to process group
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	if gracePeriod <= 0 {
		gracePeriod = 500 * time.Millisecond
	}

	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err != nil {
			// Process group exited
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}

	// If process group is still alive, send SIGKILL
	if err := syscall.Kill(-pgid, 0); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}

	return nil
}

// limitedWriter writes up to limit bytes into buf, silently discarding
// the rest.  This prevents unbounded memory growth from verbose or
// misbehaving subprocesses while retaining the initial output for
// diagnostics.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	consumed := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard silently
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, err := w.buf.Write(p)
	return consumed, err
}

// NewHardenedExecAllocator configures chromedp to run Chromium inside an isolated
// process group (Setpgid=true) and ensures complete process group cleanup upon cancellation.
func NewHardenedExecAllocator(
	ctx context.Context,
	opts ...chromedp.ExecAllocatorOption,
) (context.Context, context.CancelFunc) {
	cmdOpt := chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setpgid = true

		origCancel := cmd.Cancel
		cmd.Cancel = func() error {
			if cmd.Process != nil && cmd.Process.Pid > 1 {
				_ = KillProcessGroup(cmd.Process.Pid, 500*time.Millisecond)
			}
			if origCancel != nil {
				return origCancel()
			}
			return nil
		}
	})

	hardenedOpts := make([]chromedp.ExecAllocatorOption, 0, len(opts)+1)
	hardenedOpts = append(hardenedOpts, cmdOpt)
	hardenedOpts = append(hardenedOpts, opts...)

	return chromedp.NewExecAllocator(ctx, hardenedOpts...)
}
