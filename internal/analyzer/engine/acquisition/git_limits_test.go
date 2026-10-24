package acquisition

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitAcquisitionGrowthLimitsAndCleanup(t *testing.T) {
	for _, kind := range []string{"objects", "checkout", "files", "fast"} {
		t.Run(kind, func(t *testing.T) {
			sandbox, err := NewSandbox(t.TempDir(), "growth")
			require.NoError(t, err)
			bin := filepath.Join(t.TempDir(), "git")
			action := "/bin/mkdir -p '" + sandbox.RootPath + "/.git/objects'; /usr/bin/truncate -s 65536 '" + sandbox.RootPath + "/.git/objects/pack'"
			if kind == "checkout" {
				action = "/usr/bin/truncate -s 65536 '" + sandbox.RootPath + "/file'"
			}
			if kind == "files" {
				action = "/usr/bin/touch '" + sandbox.RootPath + "/a' '" + sandbox.RootPath + "/b' '" + sandbox.RootPath + "/c'"
			}
			tail := "\n/bin/sleep 30 &\nwait\n"
			if kind == "fast" {
				tail = "\nexit 0\n"
			}
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+action+tail), 0700))
			limits := AcquisitionLimits{MaxExtractedBytes: 32768, MaxFileCount: 2, GitTimeout: 3 * time.Second}
			_, err = cloneGitRepository(context.Background(), "https://8.8.8.8/repo.git", sandbox, limits, bin)
			require.ErrorIs(t, err, ErrGitResourceLimit)
			require.True(t, sandbox.IsClosed())
			_, err = os.Stat(sandbox.RootPath)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestGitCancellationTerminatesDescendantsAndCleans(t *testing.T) {
	sandbox, err := NewSandbox(t.TempDir(), "cancel")
	require.NoError(t, err)
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	bin := filepath.Join(t.TempDir(), "git")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n/bin/sleep 30 &\necho $! > '"+pidFile+"'\nwait\n"), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cloneGitRepository(ctx, "https://8.8.8.8/repo.git", sandbox, DefaultAcquisitionLimits(), bin)
		done <- err
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrGitAcquisitionFailed)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled process tree survived")
	}
	require.True(t, sandbox.IsClosed())
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	// A killed child can temporarily remain a zombie until the host init reaps it.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || err != nil || strings.Contains(string(data), ") Z ")
	}, time.Second, 10*time.Millisecond)
}

func TestGitWorkspaceExactLimitsAndEnvironment(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("abc"), 0600))
	n, b, err := checkGitWorkspace(context.Background(), root, AcquisitionLimits{MaxFileCount: 1, MaxExtractedBytes: 16384})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.EqualValues(t, 3, b)
	defaults := effectiveGitLimits(AcquisitionLimits{})
	require.Positive(t, defaults.MaxFileCount)
	require.Positive(t, defaults.MaxExtractedBytes)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.sslVerify")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("HTTPS_PROXY", "http://untrusted.invalid")
	t.Setenv("NO_PROXY", "*")
	t.Setenv("GIT_EXEC_PATH", "/untrusted")
	env := strings.Join(isolatedGitEnv(root), "\n")
	for _, bad := range []string{"GIT_CONFIG_COUNT", "GIT_EXEC_PATH", "PROXY", "sslVerify=false"} {
		require.NotContains(t, env, bad)
	}
	require.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
	require.Contains(t, env, "PATH=/usr/bin:/bin")
}
