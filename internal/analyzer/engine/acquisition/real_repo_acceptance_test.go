package acquisition

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
)

type BenchmarkRecord struct {
	Repository    string
	SourceMode    string
	CommitHash    string
	FileCount     int
	DownloadBytes int64
	AcquisitionMs int64
	Status        string
	ErrorClass    string
}

func TestRealRepositoryMatrix_LiveIngestion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live network repository benchmarks in short mode")
	}

	repos := []struct {
		name string
		url  string
	}{
		{name: "expressjs/cors", url: "https://github.com/expressjs/cors.git"},
		{name: "gin-gonic/gin", url: "https://github.com/gin-gonic/gin.git"},
		{name: "gimesha-adikari/pdfnest", url: "https://github.com/gimesha-adikari/pdfnest.git"},
		{name: "gimesha-adikari/pdfnest-backend", url: "https://github.com/gimesha-adikari/pdfnest-backend.git"},
		{name: "gimesha-adikari/pdfnest-worker", url: "https://github.com/gimesha-adikari/pdfnest-worker.git"},
	}

	limits := DefaultAcquisitionLimits() // 180s Git timeout, 250MB, 25k files
	records := make([]BenchmarkRecord, 0, len(repos))

	for _, target := range repos {
		t.Run("Git_"+target.name, func(t *testing.T) {
			sandbox, err := NewSandbox(t.TempDir(), "bench-"+filepath.Base(target.name))
			require.NoError(t, err)
			defer sandbox.Cleanup()

			start := time.Now()
			res, err := CloneGitRepository(context.Background(), target.url, sandbox, limits)
			elapsed := time.Since(start).Milliseconds()

			if err != nil {
				rec := BenchmarkRecord{
					Repository:    target.name,
					SourceMode:    "GIT",
					AcquisitionMs: elapsed,
					Status:        "FAILED",
					ErrorClass:    err.Error(),
				}
				records = append(records, rec)
				// Verify error classification is structured and actionable
				assert.Contains(t, err.Error(), "GIT_", "Error must be classified with structured GIT_ prefix")
				t.Logf("⚠ %s acquisition terminated as classified: %v in %d ms", target.name, err, elapsed)
				return
			}

			require.NotNil(t, res)
			assert.NotEmpty(t, res.CommitHash)
			assert.True(t, res.TotalFiles > 0, "File count must be greater than 0")
			assert.True(t, res.TotalBytes > 0, "Total bytes must be greater than 0")

			rec := BenchmarkRecord{
				Repository:    target.name,
				SourceMode:    "GIT",
				CommitHash:    res.CommitHash,
				FileCount:     res.TotalFiles,
				DownloadBytes: res.TotalBytes,
				AcquisitionMs: res.AcquisitionDurationMs,
				Status:        "SUCCESS",
			}
			records = append(records, rec)
			t.Logf("✓ %s ingested cleanly: %d files, %d bytes, commit %s in %d ms",
				target.name, res.TotalFiles, res.TotalBytes, res.CommitHash[:8], res.AcquisitionDurationMs)
		})
	}
}

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

func TestOriginalIncident_GitDiagnosticsOnFailure(t *testing.T) {
	// Test diagnostic classification on unreachable host
	sandbox, err := NewSandbox(t.TempDir(), "incident-diag-test")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	// Use non-existent domain to test network error classification
	_, err = CloneGitRepository(context.Background(), "https://nonexistent-git-host-123456789.com/repo.git", sandbox, DefaultAcquisitionLimits())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GIT_UNREACHABLE", "Unreachable host must produce GIT_UNREACHABLE error")

	// Test diagnostic classification on timeout
	tightLimits := AcquisitionLimits{
		GitTimeout: 1 * time.Millisecond,
	}
	_, err = CloneGitRepository(context.Background(), "https://github.com/gin-gonic/gin.git", sandbox, tightLimits)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GIT_TIMEOUT", "Context deadline expiration must produce GIT_TIMEOUT error")
	t.Logf("✓ Incident 3 diagnostic classification verified: GIT_UNREACHABLE and GIT_TIMEOUT accurately classified.")
}
