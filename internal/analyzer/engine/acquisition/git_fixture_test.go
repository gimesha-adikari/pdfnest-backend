package acquisition

import (
	"context"
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

	sandbox, err := NewSandbox(t.TempDir(), "local-git-fixture")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	fixtureURL := "file://" + filepath.ToSlash(repository)
	result, err := cloneGitRepository(context.Background(), fixtureURL, sandbox, AcquisitionLimits{GitTimeout: 10 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, expectedHead, result.CommitHash)
	assert.Equal(t, "fixture-repository", result.RepositoryName)
	assert.GreaterOrEqual(t, result.TotalFiles, 3)
	assert.Greater(t, result.TotalBytes, int64(0))

	assert.Equal(t, "main", strings.TrimSpace(runFixtureGit(t, "-C", sandbox.RootPath, "branch", "--show-current")))
	assert.Equal(t, "1", strings.TrimSpace(runFixtureGit(t, "-C", sandbox.RootPath, "rev-list", "--count", "HEAD")), "production shallow clone remains depth one")
	_, err = os.Stat(filepath.Join(sandbox.RootPath, "feature-only.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist, "single-branch clone must not ingest feature-only files")
	content, err := os.ReadFile(filepath.Join(sandbox.RootPath, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "second revision\n", string(content))
}

func writeFixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func runFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), output)
	return string(output)
}
