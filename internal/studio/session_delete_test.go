package studio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

type flakyCleanupDeleter struct {
	attempts int
}

func (d *flakyCleanupDeleter) DeleteObject(context.Context, string) error {
	d.attempts++
	if d.attempts == 1 {
		return errors.New("temporary object-store failure")
	}
	return nil
}

func cleanupTaskCountForKeys(t *testing.T, repo Repository, keys ...string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, repo.(*gormRepository).db.Model(&models.StudioStorageCleanupTask{}).Where("object_key IN ?", keys).Count(&count).Error)
	return count
}

func TestService_DeleteSessionRequiresOwnerAndRemovesWorkspace(t *testing.T) {
	svc, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	owner := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	foreign := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	assetID := "delete-source-" + uuid.NewString()
	model := vdm.DocumentModel{
		DocumentID: uuid.NewString(),
		PageCount:  1,
		Pages:      []vdm.PageDescriptor{{PageID: "delete-page-" + uuid.NewString(), SourcePageNumber: 1, SourceAssetID: &assetID, Dimensions: &vdm.Dimensions{Width: 612, Height: 792}}},
	}
	_, session, _, err := svc.CreateDocument(ctx, owner, "discard-me.pdf", 12, 1, assetID, "studio/test/discard-me.pdf", model)
	require.NoError(t, err)

	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, foreign), ErrUnauthorized)
	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, identity.Identity{ID: "guest", Type: identity.TypeGuest}), ErrUnauthorized)
	require.NoError(t, svc.DeleteSession(ctx, session.ID, owner))
	_, err = repo.GetSession(ctx, session.ID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	_, err = repo.GetDocument(ctx, session.DocumentID)
	assert.ErrorIs(t, err, ErrDocumentNotFound)

	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, owner), ErrSessionNotFound, "repeated deletion follows the existing not-found convention")
}

func TestRepository_DeleteSessionWorkspaceRemovesAssociatedRows(t *testing.T) {
	svc, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	owner := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	assetID := "delete-associated-" + uuid.NewString()
	model := vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: "associated-page-" + uuid.NewString(), SourcePageNumber: 1, SourceAssetID: &assetID, Dimensions: &vdm.Dimensions{Width: 612, Height: 792}}}}
	doc, session, version, err := svc.CreateDocument(ctx, owner, "associated.pdf", 10, 1, assetID, "studio/test/associated.pdf", model)
	require.NoError(t, err)
	require.NoError(t, repo.CreateExport(ctx, &models.StudioExport{ID: uuid.New(), DocumentID: doc.ID, VersionID: version.ID, ExportFormat: "pdf", R2Key: "studio/test/export.pdf", ByteSize: 1, ExpiresAt: version.CreatedAt.Add(24 * time.Hour), CreatedAt: version.CreatedAt}))
	require.NoError(t, repo.(*gormRepository).db.Create(&models.StudioJob{ID: uuid.New(), DocumentID: doc.ID, SessionID: session.ID, BaseVersionID: version.ID, WorkerJobID: "worker-job-" + uuid.NewString(), JobType: "markup_highlight", Status: "failed", IdempotencyKey: "cleanup-test-" + uuid.NewString(), Parameters: models.JSON(`{}`), SourceKey: "studio/test/staging-source.pdf", PayloadKey: "studio/test/staging-payload.json", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error)
	svc.(*studioService).cleanupWorker = NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error {
		return errors.New("deletion intentionally deferred for graph assertion")
	}))

	require.NoError(t, svc.DeleteSession(ctx, session.ID, owner))
	var count int64
	// The document cascade is the authoritative assertion for all document-owned
	// rows; the explicit export assertion protects the ephemeral resource path.
	db := repo.(*gormRepository).db
	require.NoError(t, db.Model(&models.StudioExport{}).Where("document_id = ?", doc.ID).Count(&count).Error)
	assert.Zero(t, count)
	assert.Equal(t, int64(4), cleanupTaskCountForKeys(t, repo, "studio/test/associated.pdf", "studio/test/export.pdf", "studio/test/staging-source.pdf", "studio/test/staging-payload.json"), "source, export, and both staging keys are durable cleanup work")
}

func TestStorageCleanupFailurePersistsAndRetrySurvivesWorkerRecreation(t *testing.T) {
	svcInterface, repo := getTestServiceAndRepository(t)
	svc := svcInterface.(*studioService)
	deleter := &flakyCleanupDeleter{}
	worker := NewStorageCleanupWorker(repo, deleter)
	svc.cleanupWorker = worker
	ctx := context.Background()
	owner := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	assetID := "durable-cleanup-" + uuid.NewString()
	model := vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: "durable-page-" + uuid.NewString(), SourcePageNumber: 1, SourceAssetID: &assetID}}}
	_, session, _, err := svc.CreateDocument(ctx, owner, "durable-cleanup.pdf", 10, 1, assetID, "studio/test/durable-source.pdf", model)
	require.NoError(t, err)

	require.NoError(t, svc.DeleteSession(ctx, session.ID, owner))
	assert.Equal(t, 1, deleter.attempts)
	assert.Equal(t, int64(1), cleanupTaskCountForKeys(t, repo, "studio/test/durable-source.pdf"))
	_, err = repo.GetSession(ctx, session.ID)
	assert.ErrorIs(t, err, ErrSessionNotFound, "logical deletion is complete despite physical cleanup failure")

	var pending models.StudioStorageCleanupTask
	require.NoError(t, repo.(*gormRepository).db.Where("object_key = ?", "studio/test/durable-source.pdf").First(&pending).Error)
	restartedWorker := NewStorageCleanupWorker(repo, deleter)
	restartedWorker.RunTaskAt(ctx, pending, pending.NextAttemptAt.Add(storageCleanupLease+time.Second))
	assert.Equal(t, 2, deleter.attempts)
	assert.Zero(t, cleanupTaskCountForKeys(t, repo, "studio/test/durable-source.pdf"), "successful retry removes the durable intent")
}

func TestStorageCleanupAlreadyMissingObjectResolves(t *testing.T) {
	_, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	tasks, err := repo.CreateStorageCleanupTasks(ctx, []string{"studio/test/already-missing.pdf"})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	worker := NewStorageCleanupWorker(repo, StorageObjectDeleterFunc(func(context.Context, string) error { return nil }))
	assert.Equal(t, 1, worker.RunOnceAt(ctx, time.Now().UTC()))
	assert.Zero(t, cleanupTaskCountForKeys(t, repo, "studio/test/already-missing.pdf"))
}
