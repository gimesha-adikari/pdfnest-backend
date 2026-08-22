package acquisition

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
)

func createTestZipBytes(t *testing.T) []byte {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	files := map[string]string{
		"package.json": `{"name": "test-pkg", "version": "1.0.0"}`,
		"src/index.ts": `console.log("hello world");`,
	}

	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestStorageResolution_AcrossWorkingDirectories(t *testing.T) {
	// Set up temporary local storage directory
	tempStorageDir := t.TempDir()
	t.Setenv("ANALYZER_STORAGE_DIR", tempStorageDir)

	zipBytes := createTestZipBytes(t)
	storageKey := "repositories/raw/test-cwd-archive.zip"

	// 1. Save stream to local storage
	written, sha, err := storage.SaveLocalStream(context.Background(), storageKey, bytes.NewReader(zipBytes))
	require.NoError(t, err)
	assert.Equal(t, int64(len(zipBytes)), written)
	assert.NotEmpty(t, sha)

	// Verify object exists
	assert.True(t, storage.ObjectExists(context.Background(), storageKey))

	// 2. Test resolution across 3 distinct working directories:
	// A) Current directory
	// B) Parent/Root directory
	// C) Arbitrary temporary directory
	origCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(origCwd) }()

	targetDirs := []string{
		origCwd,
		filepath.Dir(origCwd),
		t.TempDir(),
	}

	for _, dir := range targetDirs {
		t.Run("CWD_"+filepath.Base(dir), func(t *testing.T) {
			require.NoError(t, os.Chdir(dir))

			sandbox, err := NewSandbox(t.TempDir(), "test-cwd-session")
			require.NoError(t, err)
			defer sandbox.Cleanup()

			res, err := ExtractZipArchive(context.Background(), storageKey, sandbox, DefaultAcquisitionLimits())
			require.NoError(t, err, "ExtractZipArchive must succeed regardless of working directory")
			assert.Equal(t, 2, res.TotalFiles)
			assert.True(t, res.TotalBytes > 0)

			// Verify extracted file exists in sandbox
			content, err := os.ReadFile(filepath.Join(sandbox.RootPath, "package.json"))
			require.NoError(t, err)
			assert.Contains(t, string(content), "test-pkg")
		})
	}
}

func TestStorageResolution_MissingObjectRejection(t *testing.T) {
	tempStorageDir := t.TempDir()
	t.Setenv("ANALYZER_STORAGE_DIR", tempStorageDir)

	sandbox, err := NewSandbox(t.TempDir(), "test-missing-session")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	_, err = ExtractZipArchive(context.Background(), "repositories/raw/non-existent-file.zip", sandbox, DefaultAcquisitionLimits())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be found locally or in remote storage")
}
