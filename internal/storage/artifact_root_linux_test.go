//go:build linux

package storage

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/temp"
)

func rootGuardEnvironment(t *testing.T) string {
	t.Helper()
	system := filepath.Join(t.TempDir(), "system-temp")
	require.NoError(t, os.Mkdir(system, 0700))
	t.Setenv("TMPDIR", system)
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	temp.ResetDirForTesting()
	t.Cleanup(temp.ResetDirForTesting)
	return system
}
func TestArtifactLocalRootDefaultRejected(t *testing.T) {
	system := rootGuardEnvironment(t)
	t.Setenv("LOCAL_STORAGE_DIR", "")
	t.Setenv("ANALYZER_STORAGE_DIR", "")
	key, source, digest, size := createFixture(t)
	err := CreateArtifactIfAbsent(context.Background(), key, source, digest, size)
	requireCreateKind(t, err, ArtifactCreateUnsafeRoot)
	_, err = os.Stat(filepath.Join(system, "pdfnest-storage", "ocr-finalization"))
	require.True(t, os.IsNotExist(err))
}
func TestArtifactLocalRootBoundaries(t *testing.T) {
	system := rootGuardEnvironment(t)
	dedicated := temp.GetDir()
	key, source, digest, size := createFixture(t)
	for _, root := range []string{system, filepath.Join(system, "pdfnest-storage", "nested"), dedicated, filepath.Join(dedicated, "ordinary", "nested")} {
		require.NoError(t, os.MkdirAll(root, 0700))
		requireCreateKind(t, createLocalArtifact(context.Background(), root, key, source, digest, size, localCreateOps{}), ArtifactCreateUnsafeRoot)
		_, err := os.Stat(filepath.Join(root, "ocr-finalization"))
		require.True(t, os.IsNotExist(err))
	}
	// A lexical sibling is outside the boundary.
	safe := system + "-sibling"
	require.NoError(t, os.Mkdir(safe, 0700))
	t.Setenv("LOCAL_STORAGE_DIR", safe)
	require.NoError(t, CreateArtifactIfAbsent(context.Background(), key, source, digest, size))
	snapshot, err := ReadArtifact(context.Background(), key, size)
	require.NoError(t, err)
	actual, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	expected, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.NoError(t, snapshot.Cleanup())
}
func TestArtifactLocalRootSymlinksRejected(t *testing.T) {
	system := rootGuardEnvironment(t)
	key, source, digest, size := createFixture(t)
	target := filepath.Join(system, "pdfnest-storage")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "child"), 0700))
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(target, alias))
	loop := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(loop, loop))
	for _, root := range []string{alias, filepath.Join(alias, "child"), loop, filepath.Join(t.TempDir(), "missing"), target + "/.", source} {
		requireCreateKind(t, createLocalArtifact(context.Background(), root, key, source, digest, size, localCreateOps{}), ArtifactCreateUnsafeRoot)
	}
	_, err := os.Stat(filepath.Join(target, "ocr-finalization"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(target, "child", "ocr-finalization"))
	require.True(t, os.IsNotExist(err))
}

func TestArtifactLocalRootSweeperPolicy(t *testing.T) {
	// Read source only; never execute cleanup against any directory.
	source, err := parser.ParseFile(token.NewFileSet(), "../tasks/cleanup.go", nil, 0)
	require.NoError(t, err)
	var prefixes []string
	ast.Inspect(source, func(node ast.Node) bool {
		declaration, ok := node.(*ast.ValueSpec)
		if !ok || len(declaration.Names) != 1 || declaration.Names[0].Name != "allowedTempPrefixes" {
			return true
		}
		values := declaration.Values[0].(*ast.CompositeLit)
		for _, entry := range values.Elts {
			value, err := strconv.Unquote(entry.(*ast.BasicLit).Value)
			require.NoError(t, err)
			prefixes = append(prefixes, value)
		}
		return false
	})
	require.Equal(t, prefixes, artifactSweepPrefixes)
	system := rootGuardEnvironment(t)
	dedicated := temp.GetDir()
	for _, prefix := range prefixes {
		require.True(t, sweepableArtifactRoot(filepath.Join(system, prefix+"owned", "nested"), system, dedicated), prefix)
	}
	require.False(t, sweepableArtifactRoot(filepath.Join(system, "pdfnest-temp-other", "nested"), system, dedicated), "current shared scan exempts these workspaces")
	require.False(t, sweepableArtifactRoot(filepath.Join(dedicated, "pdfnest-temp-exempt", "nested"), system, dedicated), "current dedicated scan exempts these children")
	require.False(t, sweepableArtifactRoot(dedicated+"-sibling", system, dedicated), "sibling is not a dedicated child")
	require.False(t, sweepableArtifactRoot(filepath.Join(system, "safe", "nested"), system, system), "fallback to system temp does not enable a dedicated scan")
}
