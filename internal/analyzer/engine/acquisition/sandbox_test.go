package acquisition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSandboxLifecycle(t *testing.T) {
	baseDir := t.TempDir()
	analysisID := "test-session-12345"

	sandbox, err := NewSandbox(baseDir, analysisID)
	require.NoError(t, err)
	require.NotNil(t, sandbox)

	assert.Equal(t, analysisID, sandbox.ID)
	assert.DirExists(t, sandbox.RootPath)
	assert.False(t, sandbox.IsClosed())

	// Test Path Resolution Inside Root
	resPath, err := sandbox.ResolvePath("src/index.ts")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(sandbox.RootPath, "src", "index.ts"), resPath)

	// Test Traversal Rejection
	_, err = sandbox.ResolvePath("../escape.txt")
	assert.ErrorIs(t, err, ErrSandboxEscape)

	_, err = sandbox.ResolvePath("../../etc/passwd")
	assert.ErrorIs(t, err, ErrSandboxEscape)

	// Test Prefix Collision Rejection (/tmp/platen_repo_123_evil)
	evilPath := sandbox.RootPath + "_evil/file.txt"
	relEvil, _ := filepath.Rel(sandbox.RootPath, evilPath)
	_, err = sandbox.ResolvePath(relEvil)
	assert.ErrorIs(t, err, ErrSandboxEscape)

	// Test Cleanup
	err = sandbox.Cleanup()
	require.NoError(t, err)
	assert.True(t, sandbox.IsClosed())
	assert.NoDirExists(t, sandbox.RootPath)

	// Post-cleanup operations must fail cleanly
	_, err = sandbox.ResolvePath("src/index.ts")
	assert.ErrorIs(t, err, ErrSandboxClosed)

	// Idempotent cleanup
	assert.NoError(t, sandbox.Cleanup())
}

func TestSandboxRepeatedSessionKeepsIndependentExecutions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "new-base")
	first, err := NewSandbox(base, "same-session")
	require.NoError(t, err)
	defer first.Cleanup()
	canary := filepath.Join(first.RootPath, "source")
	require.NoError(t, os.WriteFile(canary, []byte("preserve"), 0600))
	second, err := NewSandbox(base, "same-session")
	require.NoError(t, err)
	require.NotEqual(t, first.scratchPath, second.scratchPath)
	_, home, temp, err := second.gitScratch()
	require.NoError(t, err)
	require.Equal(t, second.scratchPath, filepath.Dir(home))
	require.Equal(t, second.scratchPath, filepath.Dir(temp))
	require.NoError(t, second.Cleanup())
	require.NoDirExists(t, second.scratchPath)
	content, err := os.ReadFile(canary)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(content))
}

func TestInvalidSandboxIDs(t *testing.T) {
	baseDir := t.TempDir()

	invalidIDs := []string{
		"../escape",
		"../../root",
		"bad/slash",
		"bad\\backslash",
		"",
		"with spaces",
		"with$special*chars",
	}

	for _, badID := range invalidIDs {
		t.Run("Reject: "+badID, func(t *testing.T) {
			sb, err := NewSandbox(baseDir, badID)
			assert.ErrorIs(t, err, ErrInvalidAnalysisID)
			assert.Nil(t, sb)
		})
	}
}
