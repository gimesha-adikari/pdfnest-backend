package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func isPIDAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

func findChildPIDs(parentPID int) []int {
	var children []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return children
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(statBytes))
		if len(fields) >= 4 {
			ppid, err := strconv.Atoi(fields[3])
			if err == nil && ppid == parentPID {
				children = append(children, pid)
			}
		}
	}
	return children
}

func TestRunner_NormalExit(t *testing.T) {
	r := Runner{}
	ctx := context.Background()

	out, err := r.Run(ctx, 5*time.Second, "echo", "hello", "world")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello world" {
		t.Fatalf("unexpected output: %s", string(out))
	}
}

func TestRunner_ContextCancellation(t *testing.T) {
	r := Runner{GracePeriod: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := r.Run(ctx, 10*time.Second, "sleep", "10")
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if !errors.Is(err, ErrProcessCancelled) {
		t.Fatalf("expected ErrProcessCancelled, got: %v", err)
	}
}

func TestRunner_Timeout(t *testing.T) {
	r := Runner{GracePeriod: 100 * time.Millisecond}
	ctx := context.Background()

	start := time.Now()
	_, err := r.Run(ctx, 200*time.Millisecond, "sleep", "10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrProcessTimeout) {
		t.Fatalf("expected ErrProcessTimeout, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRunner_GrandchildProcessTreeTermination(t *testing.T) {
	r := Runner{GracePeriod: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	pidFile := fmt.Sprintf("/tmp/test_pgid_%d.txt", time.Now().UnixNano())
	defer os.Remove(pidFile)

	// Bash script writes parent PID and child/grandchild PIDs to file, then sleeps
	cmdStr := fmt.Sprintf(`bash -c 'echo $$ > %s; (sh -c "sleep 100" & echo $! >> %s); sleep 100'`, pidFile, pidFile)

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, 10*time.Second, "bash", "-c", cmdStr)
		done <- err
	}()

	// Wait for PIDs to be written to file
	var pids []int
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		data, err := os.ReadFile(pidFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				for _, l := range lines {
					if pidVal, err := strconv.Atoi(strings.TrimSpace(l)); err == nil && pidVal > 1 {
						pids = append(pids, pidVal)
					}
				}
				if len(pids) >= 2 {
					break
				}
			}
		}
	}

	if len(pids) < 2 {
		t.Fatalf("failed to record process group PIDs from %s", pidFile)
	}

	// Also find any grandchildren spawned
	var allPIDs []int
	allPIDs = append(allPIDs, pids...)
	for _, pid := range pids {
		allPIDs = append(allPIDs, findChildPIDs(pid)...)
	}

	// Cancel the context to trigger process group SIGTERM -> SIGKILL
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrProcessCancelled) {
			t.Fatalf("expected ErrProcessCancelled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner failed to exit after cancellation")
	}

	time.Sleep(200 * time.Millisecond)

	// Verify all processes in the process tree are dead
	for _, pid := range allPIDs {
		if isPIDAlive(pid) {
			t.Fatalf("process tree PID %d is still alive after process group termination", pid)
		}
	}
}

func TestRunner_RepeatedCancellation(t *testing.T) {
	r := Runner{GracePeriod: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		for i := 0; i < 5; i++ {
			cancel()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, err := r.Run(ctx, 5*time.Second, "sleep", "5")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrProcessCancelled) {
		t.Fatalf("expected ErrProcessCancelled, got: %v", err)
	}
}

func TestRunner_AlreadyExited(t *testing.T) {
	r := Runner{GracePeriod: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	out, err := r.Run(ctx, 5*time.Second, "true")
	if err != nil {
		t.Fatalf("expected clean exit, got %v", err)
	}
	_ = out
	cancel() // cancel after exit should be harmless
}

func TestKillProcessGroup_InvalidPID(t *testing.T) {
	// Invalid PIDs must safely no-op
	if err := KillProcessGroup(0, 100*time.Millisecond); err != nil {
		t.Fatalf("KillProcessGroup(0) should return nil, got %v", err)
	}
	if err := KillProcessGroup(1, 100*time.Millisecond); err != nil {
		t.Fatalf("KillProcessGroup(1) should return nil, got %v", err)
	}
	if err := KillProcessGroup(-5, 100*time.Millisecond); err != nil {
		t.Fatalf("KillProcessGroup(-5) should return nil, got %v", err)
	}
}

func TestRunner_ConcurrentProcessGroups(t *testing.T) {
	r := Runner{GracePeriod: 100 * time.Millisecond}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)

	go func() {
		_, err := r.Run(ctxA, 10*time.Second, "sleep", "10")
		doneA <- err
	}()

	go func() {
		_, err := r.Run(ctxB, 10*time.Second, "sleep", "10")
		doneB <- err
	}()

	time.Sleep(100 * time.Millisecond)

	// Terminate A only
	cancelA()

	select {
	case errA := <-doneA:
		if !errors.Is(errA, ErrProcessCancelled) {
			t.Fatalf("expected A to be cancelled, got %v", errA)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("group A failed to terminate")
	}

	// Group B should still be running!
	select {
	case errB := <-doneB:
		t.Fatalf("group B terminated unexpectedly: %v", errB)
	default:
		// B is still running as expected
	}

	// Now terminate B
	cancelB()
	select {
	case errB := <-doneB:
		if !errors.Is(errB, ErrProcessCancelled) {
			t.Fatalf("expected B to be cancelled, got %v", errB)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("group B failed to terminate")
	}
}

func TestNewHardenedExecAllocator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	allocCtx, allocCancel := NewHardenedExecAllocator(ctx, chromedp.NoSandbox, chromedp.Headless)
	defer allocCancel()

	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()

	var title string
	err := chromedp.Run(taskCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Title(&title),
	)
	if err != nil {
		t.Fatalf("chromedp hardened execution failed: %v", err)
	}

	// Verify cleanup works cleanly without errors
	allocCancel()
}
