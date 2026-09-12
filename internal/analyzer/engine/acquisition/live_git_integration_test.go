//go:build integration

package acquisition

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestRealRepositoryMatrix_LiveIngestion is deliberately integration-only:
// normal correctness runs must not depend on external GitHub availability or
// transfer speed. Run it explicitly with: go test -tags=integration
// ./internal/analyzer/engine/acquisition -run '^TestRealRepositoryMatrix_LiveIngestion$'.
func TestRealRepositoryMatrix_LiveIngestion(t *testing.T) {
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

	limits := DefaultAcquisitionLimits()
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
				records = append(records, BenchmarkRecord{Repository: target.name, SourceMode: "GIT", AcquisitionMs: elapsed, Status: "FAILED", ErrorClass: err.Error()})
				assert.Contains(t, err.Error(), "GIT_", "Error must be classified with structured GIT_ prefix")
				t.Logf("%s acquisition terminated as classified: %v in %d ms", target.name, err, elapsed)
				return
			}

			require.NotNil(t, res)
			assert.NotEmpty(t, res.CommitHash)
			assert.Positive(t, res.TotalFiles)
			assert.Positive(t, res.TotalBytes)
			records = append(records, BenchmarkRecord{Repository: target.name, SourceMode: "GIT", CommitHash: res.CommitHash, FileCount: res.TotalFiles, DownloadBytes: res.TotalBytes, AcquisitionMs: res.AcquisitionDurationMs, Status: "SUCCESS"})
		})
	}
}

func TestOriginalIncident_GitDiagnosticsOnFailure(t *testing.T) {
	sandbox, err := NewSandbox(t.TempDir(), "incident-diag-test")
	require.NoError(t, err)
	defer sandbox.Cleanup()

	_, err = CloneGitRepository(context.Background(), "https://nonexistent-git-host-123456789.com/repo.git", sandbox, DefaultAcquisitionLimits())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GIT_UNREACHABLE")

	tightLimits := AcquisitionLimits{GitTimeout: time.Millisecond}
	_, err = CloneGitRepository(context.Background(), "https://github.com/gin-gonic/gin.git", sandbox, tightLimits)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GIT_TIMEOUT")
}
