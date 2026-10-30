package studio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/identity"
	backendModels "pdfnest-backend/internal/models"
	"pdfnest-backend/internal/studio/models"
)

type intentWritingMaterializer struct {
	StudioVersionMaterializer
	key  string
	path string
}

func (m *intentWritingMaterializer) MaterializeVersion(ctx context.Context, session uuid.UUID, ident identity.Identity) (*MaterializedVersion, error) {
	if err := persistStudioPDF(ctx, m.path, m.key); err != nil {
		return nil, err
	}
	return m.StudioVersionMaterializer.MaterializeVersion(ctx, session, ident)
}

type admissionGateGateway struct {
	StudioWorkerGateway
	entered   chan struct{}
	release   chan struct{}
	submitErr error
	calls     int
}

func (g *admissionGateGateway) SubmitEditWithLanguage(ctx context.Context, op StudioJobName, source, payload, name string, language edit.EditorLanguageRequest) (workerJobStatus, error) {
	g.calls++
	close(g.entered)
	<-g.release
	if g.submitErr != nil {
		return workerJobStatus{}, g.submitErr
	}
	return g.StudioWorkerGateway.(StudioLanguageWorkerGateway).SubmitEditWithLanguage(ctx, op, source, payload, name, language)
}

func extractionRequest(version uuid.UUID, key string) StudioJobRequest {
	return StudioJobRequest{BaseVersionID: version, IdempotencyKey: key, Operation: StudioJobEditExtract, Parameters: []byte(`{"language_mode":"EXPLICIT","languages":["eng"]}`)}
}

func TestDurableAdmissionProtectsDispatchFromExpiryAndDuplicate(t *testing.T) {
	t.Setenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED", "true")
	f := newDurableEditorFixture(t)
	c := f.coordinator.(*studioJobCoordinator)
	nestedKey := "studio/test/nested-materialization-" + uuid.NewString()
	c.materializer = &intentWritingMaterializer{StudioVersionMaterializer: c.materializer, key: nestedKey, path: c.materializer.(*durableEditorMaterializer).path}
	gate := &admissionGateGateway{StudioWorkerGateway: f.gateway, entered: make(chan struct{}), release: make(chan struct{})}
	c.gateway = gate
	req := extractionRequest(f.baseVersion.ID, uuid.NewString())
	done := make(chan error, 1)
	go func() { _, err := c.Submit(context.Background(), f.session.ID, f.identity, req); done <- err }()
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch admission did not complete")
	}
	job, err := f.repository.FindJobByIdempotencyKey(context.Background(), f.document.ID, req.IdempotencyKey)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, "submitting", job.Status)
	require.NotEmpty(t, job.SourceKey)
	require.Contains(t, string(job.PendingObjectKeys), nestedKey)
	replay, err := c.Submit(context.Background(), f.session.ID, f.identity, req)
	require.NoError(t, err)
	require.True(t, replay.IsIdempotentReplay)
	require.Equal(t, job.ID, replay.Job.ID)
	now := time.Now().UTC()
	require.NoError(t, f.repository.(*gormRepository).db.Model(f.session).Update("expires_at", now.Add(-time.Minute)).Error)
	require.NoError(t, f.repository.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err = f.repository.GetSession(context.Background(), f.session.ID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, f.repository, job.SourceKey))
	_, err = f.repository.DeleteSessionWorkspace(context.Background(), f.session.ID, f.document.ID)
	require.ErrorIs(t, err, ErrJobReconciliationFailed)
	close(gate.release)
	require.NoError(t, <-done)
	require.Equal(t, 1, gate.calls)
	persisted, err := f.repository.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", persisted.Status)
}

func TestAmbiguousSubmissionSurvivesCancellationRestartAndExpiry(t *testing.T) {
	t.Setenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED", "true")
	f := newDurableEditorFixture(t)
	c := f.coordinator.(*studioJobCoordinator)
	gate := &admissionGateGateway{StudioWorkerGateway: f.gateway, entered: make(chan struct{}), release: make(chan struct{}), submitErr: errors.New("acknowledgement lost")}
	c.gateway = gate
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := extractionRequest(f.baseVersion.ID, uuid.NewString())
	done := make(chan error, 1)
	go func() { _, err := c.Submit(ctx, f.session.ID, f.identity, req); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	require.ErrorIs(t, <-done, ErrJobReconciliationFailed)
	job, err := f.repository.FindJobByIdempotencyKey(context.Background(), f.document.ID, req.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, "submission_uncertain", job.Status)
	restarted := *c
	replay, err := restarted.Submit(context.Background(), f.session.ID, f.identity, req)
	require.NoError(t, err)
	require.True(t, replay.IsIdempotentReplay)
	cancelled, err := restarted.Cancel(context.Background(), f.session.ID, job.ID, f.identity)
	require.NoError(t, err)
	require.Equal(t, "submission_uncertain", cancelled.Status)
	require.Empty(t, f.gateway.cancelledIDs)
	now := time.Now().UTC()
	require.NoError(t, f.repository.(*gormRepository).db.Model(f.session).Update("expires_at", now.Add(-time.Hour)).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, f.repository.EnqueueExpiredStorageCleanup(context.Background(), now.Add(time.Duration(i)*time.Hour), 50))
	}
	persisted, err := f.repository.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, job.SourceKey, persisted.SourceKey)
	require.Equal(t, 1, gate.calls)
	require.Zero(t, cleanupTaskCountForKeys(t, f.repository, job.SourceKey))
}

func TestExpiryWinsAdmissionWithoutExternalDispatch(t *testing.T) {
	t.Setenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED", "true")
	f := newDurableEditorFixture(t)
	c := f.coordinator.(*studioJobCoordinator)
	now := time.Now().UTC()
	require.NoError(t, f.repository.(*gormRepository).db.Model(f.session).Update("expires_at", now.Add(-time.Minute)).Error)
	require.NoError(t, f.repository.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err := c.Submit(context.Background(), f.session.ID, f.identity, extractionRequest(f.baseVersion.ID, uuid.NewString()))
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.Zero(t, f.gateway.editSubmits)
}

func TestAdmissionRollbackWritesNoOwnershipOrWorker(t *testing.T) {
	t.Setenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED", "true")
	f := newDurableEditorFixture(t)
	c := f.coordinator.(*studioJobCoordinator)
	db := f.repository.(*gormRepository).db
	name := "reject-admission-" + uuid.NewString()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "studio_jobs" {
			tx.AddError(errors.New("injected admission rollback"))
		}
	}))
	t.Cleanup(func() { db.Callback().Create().Remove(name) })
	req := extractionRequest(f.baseVersion.ID, uuid.NewString())
	_, err := c.Submit(context.Background(), f.session.ID, f.identity, req)
	require.Error(t, err)
	job, err := f.repository.FindJobByIdempotencyKey(context.Background(), f.document.ID, req.IdempotencyKey)
	require.NoError(t, err)
	require.Nil(t, job)
	require.Zero(t, f.gateway.editSubmits)
}

func TestStorageCleanupPreservesSharedLiveReference(t *testing.T) {
	repo, session, key := retentionWorkspace(t)
	tasks, err := repo.CreateStorageCleanupTasks(context.Background(), []string{key})
	require.NoError(t, err)
	calls := 0
	worker := NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error { calls++; return nil }))
	require.False(t, worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()))
	require.Zero(t, calls)
	_, err = repo.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	_, err = repo.DeleteSessionWorkspace(context.Background(), session.ID, session.DocumentID)
	require.NoError(t, err)
	require.True(t, worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()))
	require.Equal(t, 1, calls)
	var count int64
	require.NoError(t, repo.(*gormRepository).db.Model(&models.StudioStorageCleanupTask{}).Where("id = ?", tasks[0].ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestExpiryRetainsEveryUncertainOrUnpublishedState(t *testing.T) {
	for _, status := range []string{"preparing", "submitting", "submission_uncertain", "queued", "running", "processing", "cancel_requested", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			repo, session, key := retentionWorkspace(t)
			job := retentionJob(t, repo, session, status)
			now := time.Now().UTC()
			require.NoError(t, repo.(*gormRepository).db.Model(session).Update("expires_at", now.Add(-time.Minute)).Error)
			require.NoError(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
			_, err := repo.GetJob(context.Background(), job.ID)
			require.NoError(t, err)
			_, err = repo.GetSession(context.Background(), session.ID)
			require.NoError(t, err)
			require.Zero(t, cleanupTaskCountForKeys(t, repo, key, job.SourceKey, job.PayloadKey))
		})
	}
}

func TestPreparedObjectOwnershipSurvivesRestartAndTransfersAtTerminal(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	repo, session, _ := retentionWorkspace(t)
	job := retentionJob(t, repo, session, "running")
	key := "studio/test/interrupted-prepared-" + uuid.NewString()
	// Real local deletion fails until the durable retry is exercised below.
	path := filepath.Join(os.Getenv("LOCAL_STORAGE_DIR"), filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "held"), []byte("fixture"), 0600))
	job.PendingObjectKeys = mustJSONKey(key)
	require.NoError(t, repo.SaveJob(context.Background(), job))
	// A cleanup ledger left by an interrupted/failed publication cannot erase
	// the pending object while its durable job is still unresolved.
	tasks, err := repo.CreateStorageCleanupTasks(context.Background(), []string{key})
	require.NoError(t, err)
	calls := 0
	worker := NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error { calls++; return nil }))
	require.False(t, worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()))
	require.Zero(t, calls)
	// Recreate both repository and coordinator to exclude process-local state.
	restarted := NewRepository(repo.(*gormRepository).db)
	persisted, err := restarted.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, job.PendingObjectKeys, persisted.PendingObjectKeys)
	persisted.Status = "failed"
	require.NoError(t, restarted.SaveJob(context.Background(), persisted))
	(&studioJobCoordinator{repo: restarted}).cleanupStaging(context.Background(), persisted)
	persisted, err = restarted.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Empty(t, persisted.PendingObjectKeys)
	require.Equal(t, int64(1), cleanupTaskCountForKeys(t, restarted, key))
	require.True(t, worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()))
	require.Equal(t, 1, calls)
	require.Zero(t, cleanupTaskCountForKeys(t, restarted, key))
}

func TestExpiredSessionPreservesIndependentLiveExportRetention(t *testing.T) {
	repo, session, key := retentionWorkspace(t)
	now := time.Now().UTC()
	require.NoError(t, repo.(*gormRepository).db.Model(session).Update("expires_at", now.Add(-time.Minute)).Error)
	export := &models.StudioExport{ID: uuid.New(), DocumentID: session.DocumentID, VersionID: session.ActiveVersionID, ExportFormat: "pdf", R2Key: "studio/test/independent-export-" + uuid.NewString(), ExpiresAt: now.Add(time.Hour), CreatedAt: now, ByteSize: 1}
	require.NoError(t, repo.CreateExport(context.Background(), export))
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err := repo.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key, export.R2Key))
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now.Add(2*time.Hour), 50))
	_, err = repo.GetSession(context.Background(), session.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.Equal(t, int64(2), cleanupTaskCountForKeys(t, repo, key, export.R2Key))
}

func TestStorageRetirementSerializesPublicationAndSurvivesRestart(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	key := "studio/test/retired-" + uuid.NewString()
	tasks, err := repo.CreateStorageCleanupTasks(context.Background(), []string{key})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	worker := NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error { close(entered); <-release; return nil }))
	done := make(chan bool, 1)
	go func() { done <- worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()) }()
	<-entered // The retirement transaction has committed before external I/O.
	asset := &models.StudioAsset{ID: uuid.NewString(), DocumentID: session.DocumentID, AssetType: "source_pdf", R2Key: key, ByteSize: 1, MimeType: "application/pdf", CreatedAt: time.Now().UTC()}
	require.ErrorIs(t, repo.CreateAsset(context.Background(), asset), ErrConflict)
	close(release)
	require.True(t, <-done)
	restarted := NewRepository(repo.(*gormRepository).db)
	require.ErrorIs(t, restarted.CreateAsset(context.Background(), asset), ErrConflict, "completed retry ledger removal must never permit key reuse")
	require.Zero(t, cleanupTaskCountForKeys(t, restarted, key))
}

func TestStoragePublicationWinsBeforeRetirement(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	key := "studio/test/publication-wins-" + uuid.NewString()
	tasks, err := repo.CreateStorageCleanupTasks(context.Background(), []string{key})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	published := make(chan error, 1)
	go func() {
		published <- repo.WithTransaction(context.Background(), func(tx Repository, _ *gorm.DB) error {
			asset := &models.StudioAsset{ID: uuid.NewString(), DocumentID: session.DocumentID, AssetType: "source_pdf", R2Key: key, ByteSize: 1, MimeType: "application/pdf", CreatedAt: time.Now().UTC()}
			if err := tx.CreateAsset(context.Background(), asset); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	done := make(chan bool, 1)
	worker := NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error {
		t.Error("live publication must not be physically deleted")
		return nil
	}))
	go func() { done <- worker.RunTaskAt(context.Background(), tasks[0], time.Now().UTC()) }()
	close(release)
	require.NoError(t, <-published)
	require.False(t, <-done)
	referenced, err := repo.(*gormRepository).StorageObjectReferenced(context.Background(), key)
	require.NoError(t, err)
	require.True(t, referenced)
}

func TestSessionReclamationRequiresExplicitMixedVersionDrainGate(t *testing.T) {
	repo, session, key := retentionWorkspace(t)
	t.Setenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED", "false")
	now := time.Now().UTC()
	require.NoError(t, repo.(*gormRepository).db.Model(session).Update("expires_at", now.Add(-time.Hour)).Error)
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err := repo.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
}

func TestAcknowledgedSuccessRequiresPublicationBeforeCleanup(t *testing.T) {
	f := newDurableEditorFixture(t)
	job, _ := submitDurableCompileForReconciliation(t, f)
	f.gateway.mu.Lock()
	f.gateway.statuses[job.WorkerJobID] = workerJobStatus{ID: job.WorkerJobID, Status: "succeeded", Progress: 100}
	f.gateway.mu.Unlock()
	job.Status = "succeeded"
	require.NoError(t, f.repository.SaveJob(context.Background(), job))
	source := job.SourceKey
	f.coordinator.(*studioJobCoordinator).cleanupStaging(context.Background(), job)
	persisted, err := f.repository.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, source, persisted.SourceKey)
	restarted := *f.coordinator.(*studioJobCoordinator)
	completed, err := restarted.ReconcilePending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, completed)
	persisted, err = f.repository.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.ReconciledAt)
	require.NotNil(t, persisted.ResultVersionID)
}

func TestStorageCleanupPreservesUnresolvedTaskArtifact(t *testing.T) {
	_, repo := getTestServiceAndRepository(t)
	db := repo.(*gormRepository).db
	key := "outputs/tasks/held/result.pdf"
	row := backendModels.TaskCompletion{ID: "held", OwnerIdentity: "owner", DownloadToken: "token", State: "publishing", ResultKey: key}
	require.NoError(t, db.Create(&row).Error)
	retired, err := repo.(*gormRepository).RetireStorageObject(context.Background(), key)
	require.NoError(t, err)
	require.False(t, retired)
	require.NoError(t, db.Model(&row).Update("state", "deleted").Error)
	retired, err = repo.(*gormRepository).RetireStorageObject(context.Background(), key)
	require.NoError(t, err)
	require.True(t, retired)
}
