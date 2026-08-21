package inventory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

func TestScanRepository(t *testing.T) {
	// Create a synthetic repository in a temp directory
	tempDir := t.TempDir()

	// Create directory structure
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "src", "utils"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "node_modules", "express"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, ".git", "objects"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "certs"), 0755))

	// Create files
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "README.md"), []byte("# Test Repo"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "src", "index.ts"), []byte("console.log('hi');"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "src", "utils", "math.ts"), []byte("export const add = 1;"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "node_modules", "express", "index.js"), []byte("module.exports = {};"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".env"), []byte("SECRET=12345"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".env.example"), []byte("SECRET="), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "certs", "server.pem"), []byte("CERTIFICATE"), 0644))

	engine := exclusion.NewEngine(exclusion.Config{
		EnabledPresets: []string{"preset-node-modules"},
	})

	ctx := context.Background()
	inv, err := ScanRepository(ctx, tempDir, ScannerOptions{
		ExclusionEngine: engine,
		ArtifactSha256:  "testhash123",
		ScopeHash:       "scopehash456",
	})
	require.NoError(t, err)
	require.NotNil(t, inv)

	// Verify counts
	assert.Equal(t, "testhash123", inv.ArtifactSha256)
	assert.Equal(t, "scopehash456", inv.ScopeHash)
	assert.Greater(t, inv.TotalFiles, 0)
	assert.Equal(t, inv.IncludedFiles+inv.ExcludedFiles, inv.TotalFiles)
	assert.Equal(t, inv.IncludedBytes+inv.ExcludedBytes, inv.TotalBytes)

	// Verify manifests and languages discovered
	assert.Contains(t, inv.ManifestsFound, "package.json")
	assert.Contains(t, inv.LanguagesFound, "TypeScript")

	// Verify mandatory secret exclusions
	var foundEnv, foundEnvExample, foundPem bool
	for _, f := range inv.Files {
		if f.RelPath == ".env" {
			foundEnv = true
			assert.True(t, f.IsExcluded, ".env must be excluded")
			assert.Equal(t, exclusion.PrecedenceMandatorySecurity, f.Exclusion.Precedence)
		}
		if f.RelPath == ".env.example" {
			foundEnvExample = true
			assert.False(t, f.IsExcluded, ".env.example must be included")
		}
		if f.RelPath == "certs/server.pem" {
			foundPem = true
			assert.True(t, f.IsExcluded, "certs/server.pem must be excluded")
		}
	}
	assert.True(t, foundEnv)
	assert.True(t, foundEnvExample)
	assert.True(t, foundPem)

	// Verify deterministic alphabetical ordering
	for i := 1; i < len(inv.Files); i++ {
		assert.True(t, inv.Files[i-1].RelPath < inv.Files[i].RelPath, "files must be strictly ordered by RelPath: %s vs %s", inv.Files[i-1].RelPath, inv.Files[i].RelPath)
	}

	// Verify context cancellation
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ScanRepository(cancelCtx, tempDir, ScannerOptions{ExclusionEngine: engine})
	assert.Error(t, err, "ScanRepository should abort when context is cancelled")
}

func TestSymlinkEscapeSafety(t *testing.T) {
	tempDir := t.TempDir()
	outsideDir := t.TempDir()

	// Write sensitive file outside sandbox
	outsideSecret := filepath.Join(outsideDir, "shadow")
	require.NoError(t, os.WriteFile(outsideSecret, []byte("root:x:0:0"), 0644))

	// Create symlink inside sandbox pointing outside
	linkPath := filepath.Join(tempDir, "link_to_outside")
	err := os.Symlink(outsideSecret, linkPath)
	if err != nil {
		t.Skip("Symlinks not supported on this platform/environment")
	}

	ctx := context.Background()
	inv, err := ScanRepository(ctx, tempDir, DefaultScannerOptions(nil))
	require.NoError(t, err)

	for _, f := range inv.Files {
		if f.RelPath == "link_to_outside" {
			assert.True(t, f.IsExcluded, "symlink escaping sandbox root must be excluded")
			assert.True(t, f.IsSymlink)
		}
	}
}
