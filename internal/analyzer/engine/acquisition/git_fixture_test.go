package acquisition

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloneGitRepository_LocalFixturePreservesDefaultHeadAndFiles(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "fixture-repository")
	runFixtureGit(t, "init", repository)
	runFixtureGit(t, "-C", repository, "config", "user.email", "fixture@example.test")
	runFixtureGit(t, "-C", repository, "config", "user.name", "Fixture Test")

	writeFixtureFile(t, repository, "README.md", "first revision\n")
	writeFixtureFile(t, repository, "cmd/app/main.go", "package main\n")
	runFixtureGit(t, "-C", repository, "add", ".")
	runFixtureGit(t, "-C", repository, "commit", "-m", "initial fixture")
	runFixtureGit(t, "-C", repository, "branch", "-M", "main")

	runFixtureGit(t, "-C", repository, "checkout", "-b", "feature/fixture")
	writeFixtureFile(t, repository, "feature-only.txt", "not on default branch\n")
	runFixtureGit(t, "-C", repository, "add", ".")
	runFixtureGit(t, "-C", repository, "commit", "-m", "feature fixture")
	runFixtureGit(t, "-C", repository, "checkout", "main")

	writeFixtureFile(t, repository, "README.md", "second revision\n")
	writeFixtureFile(t, repository, "internal/history.txt", "default branch head\n")
	runFixtureGit(t, "-C", repository, "add", ".")
	runFixtureGit(t, "-C", repository, "commit", "-m", "default branch head")
	expectedHead := strings.TrimSpace(runFixtureGit(t, "-C", repository, "rev-parse", "HEAD"))

	recordPath := filepath.Join(t.TempDir(), "git-args.txt")
	bin := t.TempDir()
	gitShim := filepath.Join(bin, "git")
	shim := fmt.Sprintf(`#!/bin/bash
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [[ "${args[$i]}" == clone ]]; then
    printf '%%s\n' "$@" > %s
    for ((j=i+1; j<${#args[@]}; j++)); do
      if [[ "${args[$j]}" == https://* ]]; then args[$j]=%s; fi
    done
    exec /usr/bin/git -c protocol.file.allow=always "${args[@]}"
  fi
done
exec /usr/bin/git "$@"
`, shellQuote(recordPath), shellQuote("file://"+filepath.ToSlash(repository)))
	require.NoError(t, os.WriteFile(gitShim, []byte(shim), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	sandbox, err := NewSandbox(t.TempDir(), "local-git-fixture")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	fixtureURL := "https://8.8.8.8/fixture-repository.git"
	result, err := cloneGitRepository(context.Background(), fixtureURL, sandbox, AcquisitionLimits{GitTimeout: 10 * time.Second}, gitShim)
	require.NoError(t, err)
	assert.Equal(t, expectedHead, result.CommitHash)
	assert.Equal(t, "fixture-repository", result.RepositoryName)
	assert.GreaterOrEqual(t, result.TotalFiles, 3)
	assert.Greater(t, result.TotalBytes, int64(0))

	assert.Equal(t, "main", strings.TrimSpace(runFixtureGit(t, "-C", sandbox.RootPath, "branch", "--show-current")))
	assert.Equal(t, "1", strings.TrimSpace(runFixtureGit(t, "-C", sandbox.RootPath, "rev-list", "--count", "HEAD")), "production shallow clone remains depth one")
	args, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.Contains(t, string(args), "clone\n--depth\n1\n--no-tags\n--single-branch\n--\nhttps://8.8.8.8/fixture-repository.git\n")
	for _, option := range []string{"http.followRedirects=false", "http.proxy=http://127.0.0.1:", "http.sslVerify=true", "protocol.allow=never", "protocol.https.allow=always", "credential.helper=", "core.hooksPath=/dev/null", "init.templateDir="} {
		assert.Contains(t, string(args), option)
	}
	_, err = os.Stat(filepath.Join(sandbox.RootPath, "feature-only.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist, "single-branch clone must not ingest feature-only files")
	content, err := os.ReadFile(filepath.Join(sandbox.RootPath, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "second revision\n", string(content))
	limited, err := NewSandbox(t.TempDir(), "limited-real-fixture")
	require.NoError(t, err)
	_, err = cloneGitRepository(context.Background(), fixtureURL, limited,
		AcquisitionLimits{MaxFileCount: 2, MaxExtractedBytes: 1 << 20, GitTimeout: 5 * time.Second}, gitShim)
	require.ErrorIs(t, err, ErrGitResourceLimit, "real small clone rejects a deliberately tiny file budget")
	require.True(t, limited.IsClosed())
}

func TestCloneGitRepository_ClassifiesUnreachableTransportOutput(t *testing.T) {
	bin := t.TempDir()
	gitShim := filepath.Join(bin, "git")
	shim := "#!/bin/sh\necho 'fatal: unable to access https://fixture.invalid/repo.git: Could not resolve host: fixture.invalid' >&2\nexit 128\n"
	require.NoError(t, os.WriteFile(gitShim, []byte(shim), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	sandbox, err := NewSandbox(t.TempDir(), "unreachable-git-fixture")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	_, err = cloneGitRepository(context.Background(), "https://8.8.8.8/fixture-repository.git", sandbox, AcquisitionLimits{GitTimeout: 5 * time.Second}, gitShim)
	require.ErrorIs(t, err, ErrGitUnreachable)
}

func writeFixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func runFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("/usr/bin/git", args...)
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), output)
	return string(output)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestGitDiagnosticErrorIdentities(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		cause        error
	}{
		{"quota-like", "fatal: EDQUOT ENOSPC disk full /private/scratch", ErrGitAcquisitionFailed},
		{"generic", "fatal: /private/scratch https://fake:password@example.invalid/repo?token=fake\nremote: arbitrary\033[31m", ErrGitAcquisitionFailed},
		{"auth", "authentication failed /private/scratch", ErrGitAuthRequired},
		{"dns", "could not resolve host /private/scratch", ErrGitUnreachable},
		{"tls", "SSL certificate problem /private/scratch", ErrGitAcquisitionFailed},
		{"missing", "repository not found /private/scratch", ErrGitInvalidRepository},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox, err := NewSandbox(t.TempDir(), "diagnostic")
			require.NoError(t, err)
			shim := filepath.Join(t.TempDir(), "git")
			require.NoError(t, os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s\\n' "+shellQuote(tc.stderr)+" >&2\nexit 128\n"), 0700))
			_, err = cloneGitRepository(context.Background(), "https://8.8.8.8/fixture.git", sandbox, AcquisitionLimits{GitTimeout: time.Second}, shim)
			require.ErrorIs(t, err, tc.cause)
			require.NotErrorIs(t, err, ErrGitResourceLimit, "arbitrary output does not prove a resource quota violation")
			require.True(t, sandbox.IsClosed())
		})
	}
}
