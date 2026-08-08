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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProcessStart, err)
	}

	pgid := cmd.Process.Pid
	if pgid <= 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("%w: invalid process group ID %d", ErrProcessStart, pgid)
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case waitErr := <-done:
		if waitErr != nil {
			if ctx.Err() != nil {
				return outBuf.Bytes(), fmt.Errorf("%w: %v", ErrProcessCancelled, ctx.Err())
			}
			return outBuf.Bytes(), fmt.Errorf("%w: %v", ErrProcessExit, waitErr)
		}
		return outBuf.Bytes(), nil

	case <-runCtx.Done():
		var errReason error
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			errReason = ErrProcessTimeout
		} else {
			errReason = ErrProcessCancelled
		}

		grace := r.GracePeriod
		if grace <= 0 {
			grace = 500 * time.Millisecond
		}
		_ = KillProcessGroup(pgid, grace)

		<-done

		return outBuf.Bytes(), fmt.Errorf("%w: %v", errReason, runCtx.Err())
	}
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
