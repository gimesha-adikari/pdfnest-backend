package acquisition

import (
	"context"
	"errors"
	"net"
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
	for _, kind := range []string{"objects", "checkout", "files", "fast", "home", "temp", "aggregate", "rev-parse"} {
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
			if kind == "home" {
				action = `/usr/bin/truncate -s 65536 "$HOME/cache"`
			}
			if kind == "temp" {
				action = `/usr/bin/truncate -s 65536 "$TMPDIR/pack"`
			}
			if kind == "aggregate" {
				// Each write fits independently; the shared scratch sum must not.
				action = `/usr/bin/truncate -s 16384 "$HOME/cache" "$TMPDIR/pack" ` + shellQuote(filepath.Join(sandbox.RootPath, "source"))
			}
			tail := "\n/bin/sleep 30 &\nwait\n"
			if kind == "fast" || kind == "home" || kind == "temp" || kind == "aggregate" {
				tail = "\nexit 0\n"
			}
			if kind == "rev-parse" {
				action = `case "$*" in *rev-parse*) /usr/bin/truncate -s 65536 "$TMPDIR/revision";; esac`
				tail = "\nexit 0\n"
			}
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+action+tail), 0700))
			limits := AcquisitionLimits{MaxExtractedBytes: 32768, MaxFileCount: 2, GitTimeout: 3 * time.Second}
			_, err = cloneGitRepository(context.Background(), "https://8.8.8.8/repo.git", sandbox, limits, bin)
			require.ErrorIs(t, err, ErrGitResourceLimit)
			require.True(t, sandbox.IsClosed())
			require.NoDirExists(t, sandbox.scratchPath, "repository and auxiliary paths must be removed together")
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
	temp := filepath.Join(root, "tmp")
	env := strings.Join(isolatedGitEnv(root, temp), "\n")
	for _, bad := range []string{"GIT_CONFIG_COUNT", "GIT_EXEC_PATH", "PROXY", "sslVerify=false"} {
		require.NotContains(t, env, bad)
	}
	require.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
	require.Contains(t, env, "PATH=/usr/bin:/bin")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		require.Contains(t, env, key+"="+temp)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		require.Contains(t, env, key+"="+root)
	}
}

func TestGitHardStoragePolicyRefusesBeforeNetworkOrExecution(t *testing.T) {
	for _, policy := range []string{"required", "require", "true", "REQUIRED", " required "} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("ANALYZER_GIT_STORAGE_POLICY", policy)
			resolverCalled := false
			err := ValidateGitURLWithResolver("https://example.test/repository", func(string) ([]net.IP, error) {
				resolverCalled = true
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			})
			require.ErrorIs(t, err, ErrGitAcquisitionFailed)
			require.False(t, resolverCalled, "API/session and queued-job validation must deny before DNS")
			sandbox, err := NewSandbox(t.TempDir(), "admission")
			require.NoError(t, err)
			marker := filepath.Join(t.TempDir(), "launched")
			shim := filepath.Join(t.TempDir(), "git")
			require.NoError(t, os.WriteFile(shim, []byte("#!/bin/sh\n/usr/bin/touch "+shellQuote(marker)+"\n"), 0700))
			// A malformed URL would fail validation. Policy refusal must occur
			// first, before URL/DNS/transport or launching even the test helper.
			_, err = cloneGitRepository(context.Background(), "://invalid", sandbox, DefaultAcquisitionLimits(), shim)
			require.ErrorIs(t, err, ErrGitAcquisitionFailed)
			if policy == "required" {
				require.ErrorIs(t, err, ErrGitStorageUnavailable)
			}
			require.NotErrorIs(t, err, ErrGitResourceLimit)
			require.True(t, sandbox.IsClosed())
			require.NoFileExists(t, marker)
			require.NoDirExists(t, sandbox.scratchPath)
		})
	}
}

func TestGitAuxiliaryScratchControlPreservesRepositoryStatistics(t *testing.T) {
	t.Setenv("ANALYZER_GIT_STORAGE_POLICY", "monitored")
	sandbox, err := NewSandbox(t.TempDir(), "auxiliary-control")
	require.NoError(t, err)
	defer sandbox.Cleanup()
	shim := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
set -eu
test "$PWD" = "$HOME"
test "$TMPDIR" = "$TMP"
test "$TMPDIR" = "$TEMP"
test "$HOME" = "$XDG_CONFIG_HOME"
test "$HOME" = "$XDG_CACHE_HOME"
test "$HOME" = "$XDG_DATA_HOME"
test "$HOME" = "$XDG_STATE_HOME"
printf auxiliary > "$HOME/cache"
temporary=$(/usr/bin/mktemp)
printf temporary > "$temporary"
printf source > ` + shellQuote(filepath.Join(sandbox.RootPath, "README")) + `
case "$*" in *rev-parse*) printf 'fixture-commit\n';; esac
`
	require.NoError(t, os.WriteFile(shim, []byte(script), 0700))
	result, err := cloneGitRepository(context.Background(), "https://8.8.8.8/fixture.git", sandbox, DefaultAcquisitionLimits(), shim)
	require.NoError(t, err)
	require.Equal(t, "fixture-commit", result.CommitHash)
	require.Equal(t, 1, result.TotalFiles, "auxiliary files must not become repository provenance")
	require.EqualValues(t, len("source"), result.TotalBytes)
	require.FileExists(t, filepath.Join(sandbox.scratchPath, "home", "cache"))
	temporaries, err := os.ReadDir(filepath.Join(sandbox.scratchPath, "tmp"))
	require.NoError(t, err)
	require.Len(t, temporaries, 2, "clone and rev-parse must use the accounted temporary root")
}
