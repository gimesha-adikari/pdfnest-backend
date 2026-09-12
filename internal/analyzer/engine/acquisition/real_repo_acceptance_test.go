package acquisition

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
)

func TestOriginalIncident_ZipUploadResolution(t *testing.T) {
	// Incident 1: "open repositories/raw/pdfnest.zip: no such file or directory"
	tempStorageDir := t.TempDir()
	t.Setenv("ANALYZER_STORAGE_DIR", tempStorageDir)

	// Bundle a simulated pdfnest repo into zip
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	files := map[string]string{
		"package.json":       `{"name": "pdfnest", "version": "0.1.0", "dependencies": {"next": "16.2.7", "react": "19.0.0"}}`,
		"next.config.mjs":    `export default { reactStrictMode: true };`,
		"components/App.tsx": `export default function App() { return <div>PDFNest</div>; }`,
	}
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(w, content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	// Persist to storage key
	storageKey := "repositories/raw/pdfnest.zip"
	written, sha, err := storage.SaveLocalStream(context.Background(), storageKey, bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, int64(buf.Len()), written)
	assert.NotEmpty(t, sha)

	sandbox, err := NewSandbox(t.TempDir(), "incident-1-test")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	// Extract via storageKey
	res, err := ExtractZipArchive(context.Background(), storageKey, sandbox, DefaultAcquisitionLimits())
	require.NoError(t, err, "ExtractZipArchive must resolve storageKey without local CWD error")
	assert.Equal(t, 3, res.TotalFiles)

	pkgContent, err := os.ReadFile(filepath.Join(sandbox.RootPath, "package.json"))
	require.NoError(t, err)
	assert.Contains(t, string(pkgContent), "pdfnest")
	t.Logf("✓ Incident 1 successfully verified: pdfnest.zip resolved and extracted cleanly (%d files).", res.TotalFiles)
}
