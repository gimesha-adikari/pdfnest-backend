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
	"gorm.io/gorm/clause"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

func retentionWorkspace(t *testing.T) (Repository, *models.StudioSession, string) {
	t.Helper()
	svc, repo := getTestServiceAndRepository(t)
	sqlDB, err := repo.(*gormRepository).db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	assetID := "retention-" + uuid.NewString()
	key := "studio/test/" + assetID + ".pdf"
	_, session, _, err := svc.CreateDocument(context.Background(), identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}, "retention.pdf", 1, 1, assetID, key,
		vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: uuid.NewString(), SourceAssetID: &assetID, SourcePageNumber: 1}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := repo.DeleteSessionWorkspace(context.Background(), session.ID, session.DocumentID)
		require.NoError(t, err)
	})
	return repo, session, key
}

func retentionJob(t *testing.T, repo Repository, session *models.StudioSession, status string) *models.StudioJob {
	t.Helper()
	job := &models.StudioJob{ID: uuid.New(), DocumentID: session.DocumentID, SessionID: session.ID, BaseVersionID: session.ActiveVersionID,
		WorkerJobID: uuid.NewString(), JobType: "markup_highlight", Status: status, IdempotencyKey: uuid.NewString(), Parameters: models.JSON(`{}`),
		SourceKey: "studio/test/source-" + uuid.NewString(), PayloadKey: "studio/test/payload-" + uuid.NewString(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, repo.CreateJob(context.Background(), job))
	return job
}

func TestRetentionExpiryPreservesWorkspaceWithoutDurableSubmissionGuard(t *testing.T) {
	repo, session, sourceKey := retentionWorkspace(t)
	now := time.Now().UTC()
	require.NoError(t, repo.(*gormRepository).db.Model(session).Update("expires_at", now.Add(-time.Hour)).Error)
	// Absence of a registered job does not prove that no gateway submission is
	// in flight. Session reclamation must stay disabled until admission is
	// serialized with deletion using a durable pre-submission guard.
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err := repo.GetSession(context.Background(), session.ID)
	require.NoError(t, err)
	_, err = repo.GetDocument(context.Background(), session.DocumentID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, sourceKey))
}

func TestRetentionExpiredExportPreservesLiveWorkspaceAndExport(t *testing.T) {
	repo, session, sourceKey := retentionWorkspace(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expired := &models.StudioExport{ID: uuid.New(), DocumentID: session.DocumentID, VersionID: session.ActiveVersionID, ExportFormat: "pdf", R2Key: "studio/test/expired-" + uuid.NewString(), ByteSize: 1, ExpiresAt: now.Add(-time.Minute), CreatedAt: now}
	live := *expired
	live.ID, live.R2Key, live.ExpiresAt = uuid.New(), "studio/test/live-"+uuid.NewString(), now.Add(time.Hour)
	require.NoError(t, repo.CreateExport(ctx, expired))
	require.NoError(t, repo.CreateExport(ctx, &live))
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(ctx, now, 50))
	_, err := repo.GetExport(ctx, expired.ID)
	require.ErrorIs(t, err, ErrExportNotFound)
	_, err = repo.GetExport(ctx, live.ID)
	require.NoError(t, err)
	_, err = repo.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), cleanupTaskCountForKeys(t, repo, expired.R2Key))
	require.Zero(t, cleanupTaskCountForKeys(t, repo, sourceKey, live.R2Key))
}

func TestRetentionWorkerDeletesExpiredLocalExport(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	repo, session, _ := retentionWorkspace(t)
	now := time.Now().UTC()
	key := "studio/test/export-" + uuid.NewString()
	require.NoError(t, repo.CreateExport(context.Background(), &models.StudioExport{ID: uuid.New(), DocumentID: session.DocumentID, VersionID: session.ActiveVersionID, ExportFormat: "pdf", R2Key: key, ByteSize: 1, ExpiresAt: now.Add(-time.Minute), CreatedAt: now}))
	require.NoError(t, storage.SaveObjectBytes(context.Background(), key, []byte("synthetic"), "application/pdf"))
	worker := NewStorageCleanupWorker(repo, nil)
	worker.RunOnceAt(context.Background(), now.Add(time.Second))
	_, err := os.Stat(filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key)))
	require.True(t, errors.Is(err, os.ErrNotExist))
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
}

func TestRetentionStagingFailureKeepsDurableIntent(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	repo, session, _ := retentionWorkspace(t)
	job := retentionJob(t, repo, session, "failed")
	key := job.SourceKey
	// A nonempty directory injects a real local delete failure without using a
	// provider account or replacing the storage implementation.
	path := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "retained"), []byte("synthetic"), 0600))
	coordinator := &studioJobCoordinator{repo: repo}
	coordinator.cleanupStaging(context.Background(), job)
	persisted, err := repo.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Empty(t, persisted.SourceKey)
	require.Empty(t, persisted.PayloadKey)
	require.Equal(t, int64(1), cleanupTaskCountForKeys(t, repo, key))
	require.NoError(t, os.Remove(filepath.Join(path, "retained")))
	worker := NewStorageCleanupWorker(repo, nil)
	worker.RunOnceAt(context.Background(), time.Now().UTC().Add(time.Minute))
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRetentionStagingRefusesNonterminalJob(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	job := retentionJob(t, repo, session, "running")
	key := job.SourceKey
	(&studioJobCoordinator{repo: repo}).cleanupStaging(context.Background(), job)
	persisted, err := repo.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, key, persisted.SourceKey)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
}

func TestRetentionUnregisteredCleanupFailureKeepsIntentAfterCancellation(t *testing.T) {
	for _, pathKind := range []string{"submission", "prepared_result"} {
		t.Run(pathKind, func(t *testing.T) {
			t.Setenv("APP_ENV", "test")
			t.Setenv("STORAGE_MODE", "local")
			t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
			repo, _, _ := retentionWorkspace(t)
			key := "studio/test/rollback-" + uuid.NewString()
			path := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
			require.NoError(t, os.MkdirAll(path, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(path, "retained"), []byte("synthetic"), 0600))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			coordinator := &studioJobCoordinator{repo: repo}
			if pathKind == "prepared_result" {
				coordinator.cleanupPreparedStudioObject(ctx, key)
			} else {
				coordinator.cleanupUnregisteredObjects(ctx, key)
			}
			require.Equal(t, int64(1), cleanupTaskCountForKeys(t, repo, key))
			require.NoError(t, os.Remove(filepath.Join(path, "retained")))
			NewStorageCleanupWorker(repo, nil).RunOnceAt(context.Background(), time.Now().UTC().Add(time.Minute))
			require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
			_, err := os.Stat(path)
			require.True(t, errors.Is(err, os.ErrNotExist))
		})
	}
}

func TestRetentionStagingTransferRollsBackOnJobSaveFailure(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	job := retentionJob(t, repo, session, "failed")
	source, payload := job.SourceKey, job.PayloadKey
	db := repo.(*gormRepository).db
	// Fail exactly this job's key clearing after the ledger insert, proving
	// that the insert and reference transfer roll back together.
	require.NoError(t, db.Exec("ALTER TABLE studio_jobs ADD CONSTRAINT retention_save_failure CHECK (id <> '"+job.ID.String()+"'::uuid OR source_key <> '') NOT VALID").Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("ALTER TABLE studio_jobs DROP CONSTRAINT retention_save_failure").Error)
	})
	(&studioJobCoordinator{repo: repo}).cleanupStaging(context.Background(), job)
	persisted, err := repo.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, source, persisted.SourceKey)
	require.Equal(t, payload, persisted.PayloadKey)
	require.Equal(t, source, job.SourceKey, "caller state must also retain keys after rollback")
	require.Zero(t, cleanupTaskCountForKeys(t, repo, source, payload))
}

func TestRetentionUnregisteredCleanupStillDeletesWhenLedgerFails(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	repo, _, _ := retentionWorkspace(t)
	db := repo.(*gormRepository).db
	key := "studio/test/rollback-" + uuid.NewString()
	require.NoError(t, storage.SaveObjectBytes(context.Background(), key, []byte("synthetic"), "application/pdf"))
	require.NoError(t, db.Exec("ALTER TABLE studio_storage_cleanup_tasks ADD CONSTRAINT retention_rollback_intent_failure CHECK (object_key <> '"+key+"') NOT VALID").Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("ALTER TABLE studio_storage_cleanup_tasks DROP CONSTRAINT retention_rollback_intent_failure").Error)
	})
	(&studioJobCoordinator{repo: repo}).cleanupUnregisteredObjects(context.Background(), key)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key), "failed ledger insert must not be reported as durable intent")
	_, err := os.Stat(filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key)))
	require.True(t, errors.Is(err, os.ErrNotExist), "best-effort deletion must still run when the ledger is unavailable")
}

func TestRetentionExpiryRollsBackWhenIntentCannotPersist(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	db := repo.(*gormRepository).db
	now := time.Now().UTC()
	key := "studio/test/export-" + uuid.NewString()
	export := &models.StudioExport{ID: uuid.New(), DocumentID: session.DocumentID, VersionID: session.ActiveVersionID, ExportFormat: "pdf", R2Key: key, ByteSize: 1, ExpiresAt: now.Add(-time.Minute), CreatedAt: now}
	require.NoError(t, repo.CreateExport(context.Background(), export))
	// The key is server-created from a UUID and contains no SQL metacharacters.
	require.NoError(t, db.Exec("ALTER TABLE studio_storage_cleanup_tasks ADD CONSTRAINT retention_intent_failure CHECK (object_key <> '"+key+"') NOT VALID").Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("ALTER TABLE studio_storage_cleanup_tasks DROP CONSTRAINT retention_intent_failure").Error)
	})
	require.Error(t, repo.EnqueueExpiredStorageCleanup(context.Background(), now, 50))
	_, err := repo.GetExport(context.Background(), export.ID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
}

func TestRetentionExpirySkipsLockedExport(t *testing.T) {
	repo, session, _ := retentionWorkspace(t)
	db := repo.(*gormRepository).db
	now := time.Now().UTC()
	key := "studio/test/export-" + uuid.NewString()
	export := &models.StudioExport{ID: uuid.New(), DocumentID: session.DocumentID, VersionID: session.ActiveVersionID, ExportFormat: "pdf", R2Key: key, ByteSize: 1, ExpiresAt: now.Add(-time.Minute), CreatedAt: now}
	require.NoError(t, repo.CreateExport(context.Background(), export))
	locked := db.Begin()
	require.NoError(t, locked.Error)
	defer locked.Rollback()
	var row models.StudioExport
	require.NoError(t, locked.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", export.ID).Error)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, repo.EnqueueExpiredStorageCleanup(ctx, now, 50))
	_, err := repo.GetExport(ctx, export.ID)
	require.NoError(t, err)
	require.Zero(t, cleanupTaskCountForKeys(t, repo, key))
}
