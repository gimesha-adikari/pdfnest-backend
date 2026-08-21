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

	"pdfnest-backend/internal/analyzer/engine"
)

func createTestZip(t *testing.T, entries map[string][]byte) string {
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	for name, content := range entries {
		header := &zip.FileHeader{
			Name:   name,
			Method: zip.Deflate,
		}
		f, err := w.CreateHeader(header)
		require.NoError(t, err)
		_, err = f.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	zipFile := filepath.Join(t.TempDir(), "test_repo.zip")
	require.NoError(t, os.WriteFile(zipFile, buf.Bytes(), 0644))
	return zipFile
}

func TestExtractNormalZip(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"package.json":      []byte(`{"name":"test"}`),
		"src/index.ts":      []byte("console.log('hi');"),
		"src/utils/math.ts": []byte("export const add = 1;"),
	})

	sandbox, err := NewSandbox(t.TempDir(), "session-normal")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	res, err := ExtractZipArchive(context.Background(), zipPath, sandbox, DefaultAcquisitionLimits())
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, 3, res.TotalFiles)
	assert.NotEmpty(t, res.ArchiveSha256)
	assert.FileExists(t, filepath.Join(sandbox.RootPath, "package.json"))
	assert.FileExists(t, filepath.Join(sandbox.RootPath, "src", "index.ts"))
	assert.FileExists(t, filepath.Join(sandbox.RootPath, "src", "utils", "math.ts"))
}

func TestZipSlipRejection(t *testing.T) {
	tests := []struct {
		name      string
		entryName string
	}{
		{"Relative Traversal", "../../etc/passwd"},
		{"Deep Relative Traversal", "../../../tmp/escape.txt"},
		{"Absolute Path", "/etc/shadow"},
		{"Windows Drive Letter", "C:/evil.bat"},
		{"Nested Traversal", "src/../../outside.txt"},
		{"Backslash Traversal", "..\\..\\escape.txt"},
		{"Deep Backslash Traversal", "..\\..\\..\\tmp\\escape.txt"},
		{"Mixed Separator Traversal", "src/..\\..\\outside.txt"},
		{"Redundant Dot Traversal", "a/./b/../../../outside.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zipPath := createTestZip(t, map[string][]byte{
				tt.entryName: []byte("malicious content"),
			})

			sandbox, err := NewSandbox(t.TempDir(), "session-slip")
			require.NoError(t, err)

			_, err = ExtractZipArchive(context.Background(), zipPath, sandbox, DefaultAcquisitionLimits())
			assert.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrZipSlipDetected)

			// Verify sandbox was cleaned up automatically on failure
			assert.True(t, sandbox.IsClosed())
		})
	}
}

func TestSymlinkArchiveRejection(t *testing.T) {
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	header := &zip.FileHeader{
		Name: "symlink_entry",
	}
	header.SetMode(0755 | os.ModeSymlink)

	f, err := w.CreateHeader(header)
	require.NoError(t, err)
	_, err = f.Write([]byte("/etc/passwd"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	zipFile := filepath.Join(t.TempDir(), "symlink_test.zip")
	require.NoError(t, os.WriteFile(zipFile, buf.Bytes(), 0644))

	sandbox, err := NewSandbox(t.TempDir(), "session-symlink")
	require.NoError(t, err)

	_, err = ExtractZipArchive(context.Background(), zipFile, sandbox, DefaultAcquisitionLimits())
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrArchiveSymlinkRejected)
	assert.True(t, sandbox.IsClosed())
}

func TestDecompressionBombRatioRejection(t *testing.T) {
	// Create highly compressible repeating payload (100 KB zeros compressed into ~100 bytes -> ratio ~1000:1)
	bombPayload := bytes.Repeat([]byte{0}, 100*1024)
	zipPath := createTestZip(t, map[string][]byte{
		"bomb.bin": bombPayload,
	})

	sandbox, err := NewSandbox(t.TempDir(), "session-bomb")
	require.NoError(t, err)

	limits := DefaultAcquisitionLimits()
	limits.MaxDecompressionRatio = 10.0 // Strict 10:1 ratio limit

	_, err = ExtractZipArchive(context.Background(), zipPath, sandbox, limits)
	assert.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrDecompressionRatioExceeded)
	assert.True(t, sandbox.IsClosed(), "Sandbox must be purged on decompression bomb detection")
}

func TestMaxFileCountExceededRejection(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"file1.txt": []byte("1"),
		"file2.txt": []byte("2"),
		"file3.txt": []byte("3"),
	})

	sandbox, err := NewSandbox(t.TempDir(), "session-count")
	require.NoError(t, err)

	limits := DefaultAcquisitionLimits()
	limits.MaxFileCount = 2 // Limit to 2 files

	_, err = ExtractZipArchive(context.Background(), zipPath, sandbox, limits)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxFileCountExceeded)
	assert.True(t, sandbox.IsClosed())
}

func TestMaxExtractedSizeExceededRejection(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"file1.bin": bytes.Repeat([]byte{1}, 1024),
	})

	sandbox, err := NewSandbox(t.TempDir(), "session-size")
	require.NoError(t, err)

	limits := DefaultAcquisitionLimits()
	limits.MaxExtractedBytes = 500 // Limit to 500 bytes

	_, err = ExtractZipArchive(context.Background(), zipPath, sandbox, limits)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxExtractedSizeExceeded)
	assert.True(t, sandbox.IsClosed())
}

func TestExtractionContextCancellation(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"file1.txt": []byte("hello"),
	})

	sandbox, err := NewSandbox(t.TempDir(), "session-cancel")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancelled

	_, err = ExtractZipArchive(ctx, zipPath, sandbox, DefaultAcquisitionLimits())
	assert.Error(t, err)
	assert.True(t, sandbox.IsClosed())
}
