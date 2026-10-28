package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"log"
	"path/filepath"
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/acquisition"
	"testing"
)

func TestPublicFailureWorkerPublication(t *testing.T) {
	var diagnostic bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&diagnostic)
	defer log.SetOutput(previous)
	q := NewMemoryJobQueue(1)
	cfg := DefaultWorkerConfig()
	cfg.SandboxBaseDir = t.TempDir()
	w, err := NewAnalyzerWorker(cfg, q)
	require.NoError(t, err)
	task := &AnalyzerJob{TaskID: uuid.NewString(), SessionID: uuid.NewString(), SourceType: engine.SourceTypeZip, StagedArchivePath: filepath.Join(t.TempDir(), "synthetic-secret?token=fake-token.zip")}
	w.processJob(context.Background(), task, task.TaskID)
	p, ok := q.GetProgress(task.TaskID)
	require.True(t, ok)
	require.Equal(t, StatusFailed, p.Status)
	require.Equal(t, "Repository analysis failed.", p.ErrorMessage)
	require.Equal(t, "Analysis failed", p.StageMessage)
	_, exists := q.GetResult(task.TaskID)
	require.False(t, exists)
	require.NotContains(t, diagnostic.String(), "fake-token")
	require.NotContains(t, diagnostic.String(), task.StagedArchivePath)
	require.Contains(t, diagnostic.String(), "stage=ACQUIRING")
}

func TestPublicFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		cause   error
		message string
	}{
		{acquisition.ErrGitResourceLimit, "Repository exceeds Git acquisition resource limits."},
		{acquisition.ErrGitTimeout, "Repository acquisition timed out."},
		{acquisition.ErrGitAuthRequired, "Repository authentication is required."},
		{acquisition.ErrGitUnreachable, "Could not connect to the repository host."},
		{acquisition.ErrGitInvalidRepository, "Repository was not found or is inaccessible."},
		{acquisition.ErrSSRFBlocked, "Repository host is not permitted."},
		{acquisition.ErrUnsupportedGitProtocol, "Repository URL must use HTTPS."},
		{acquisition.ErrGitAcquisitionFailed, "Repository acquisition failed."},
		{fmt.Errorf("%w: %w", acquisition.ErrGitAcquisitionFailed, acquisition.ErrGitStorageUnavailable), "Repository acquisition failed."},
		{acquisition.ErrGitCloneFailed, "Repository acquisition failed."},
		{context.DeadlineExceeded, "Repository analysis timed out."},
		{context.Canceled, "Repository analysis was canceled."},
		{errors.New("unknown /private/scratch"), "Repository analysis failed."},
	} {
		t.Run(tc.message+tc.cause.Error(), func(t *testing.T) {
			err := fmt.Errorf("pipeline: %w: fatal /private/scratch https://fake:password@example.invalid?token=fake\nEDQUOT ENOSPC\x1b[31m", tc.cause)
			require.ErrorIs(t, err, tc.cause)
			message := PublicFailureMessage(err)
			require.Equal(t, tc.message, message)
			require.Equal(t, message, SafeStoredFailureMessage(message), "writer and legacy reader vocabularies must agree")
			// Text resembling a sentinel is not a typed error identity.
			require.Equal(t, "Repository analysis failed.", PublicFailureMessage(errors.New(err.Error())))
			q := NewMemoryJobQueue(1)
			require.NoError(t, q.PublishProgress(context.Background(), "task", TaskProgress{Status: StatusFailed, StageMessage: err.Error(), ErrorMessage: message, Result: &engine.CanonicalAnalysisResult{}}))
			p, _ := q.GetProgress("task")
			require.Equal(t, tc.message, p.ErrorMessage)
			require.Equal(t, "Analysis failed", p.StageMessage)
			require.Nil(t, p.Result)
		})
	}
}

// Panic values can contain the same diagnostics as an ordinary error. Trigger
// recovery through the actual progress callback, without a production test hook.
type panicProgressQueue struct {
	*MemoryJobQueue
	panicked bool
}

func (q *panicProgressQueue) PublishProgress(ctx context.Context, id string, p TaskProgress) error {
	if !q.panicked {
		q.panicked = true
		panic("/private/path fake-password\n\x1b[31m")
	}
	return q.MemoryJobQueue.PublishProgress(ctx, id, p)
}
func TestPublicFailureWorkerPanic(t *testing.T) {
	q := &panicProgressQueue{MemoryJobQueue: NewMemoryJobQueue(1)}
	cfg := DefaultWorkerConfig()
	cfg.SandboxBaseDir = t.TempDir()
	w, err := NewAnalyzerWorker(cfg, q)
	require.NoError(t, err)
	job := &AnalyzerJob{TaskID: uuid.NewString(), SessionID: uuid.NewString(), SourceType: engine.SourceTypeLocalFolder}
	w.processJob(context.Background(), job, job.TaskID)
	p, ok := q.GetProgress(job.TaskID)
	require.True(t, ok)
	require.Equal(t, StatusFailed, p.Status)
	require.Equal(t, "Repository analysis failed.", p.ErrorMessage)
	_, ok = q.GetResult(job.TaskID)
	require.False(t, ok)
}

func TestPublicFailureGitValidationPublication(t *testing.T) {
	q := NewMemoryJobQueue(1)
	cfg := DefaultWorkerConfig()
	cfg.SandboxBaseDir = t.TempDir()
	w, err := NewAnalyzerWorker(cfg, q)
	require.NoError(t, err)
	// This address is rejected by validation without a connection attempt.
	job := &AnalyzerJob{TaskID: uuid.NewString(), SessionID: uuid.NewString(), SourceType: engine.SourceTypeGit, GitURL: "https://127.0.0.1/private?token=fake"}
	w.processJob(context.Background(), job, job.TaskID)
	p, ok := q.GetProgress(job.TaskID)
	require.True(t, ok)
	require.Equal(t, StatusFailed, p.Status)
	require.Equal(t, "Repository host is not permitted.", p.ErrorMessage)
	_, ok = q.GetResult(job.TaskID)
	require.False(t, ok)
}
