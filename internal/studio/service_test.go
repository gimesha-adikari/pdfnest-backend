package studio

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

func getTestService(t *testing.T) Service {
	service, _ := getTestServiceAndRepository(t)
	return service
}

func getTestServiceAndRepository(t *testing.T) (Service, Repository) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to PostgreSQL")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())

	err = db.AutoMigrate(
		&models.StudioDocument{},
		&models.StudioAsset{},
		&models.StudioSnapshot{},
		&models.StudioVersion{},
		&models.StudioOperation{},
		&models.StudioSession{},
		&models.StudioExport{},
		&models.StudioStorageCleanupTask{},
		&models.StudioEditorState{},
	)
	require.NoError(t, err)

	repo := NewRepository(db)
	return NewService(repo), repo
}

func TestService_CreateDocumentAndGetSession(t *testing.T) {
	svc := getTestService(t)
	ctx := context.Background()

	guestIdent := identity.Identity{
		ID:   "guest_tok_" + uuid.New().String(),
		Type: identity.TypeGuest,
	}

	assetID := "ast_init_" + uuid.New().String()
	initVDM := vdm.DocumentModel{
		DocumentID: "doc_dummy",
		PageCount:  1,
		Pages: []vdm.PageDescriptor{
			{
				PageID:           "p1",
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Rotation:         0,
			},
		},
	}

	doc, sess, ver, err := svc.CreateDocument(
		ctx,
		guestIdent,
		"annual_report.pdf",
		2048576,
		1,
		assetID,
		"studio/sources/annual_report.pdf",
		initVDM,
	)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, doc.ID)
	assert.NotEqual(t, uuid.Nil, sess.ID)
	assert.NotEqual(t, uuid.Nil, ver.ID)
	assert.Equal(t, 0, ver.VersionNumber)
	assert.Equal(t, "ready", ver.Status)
	assert.Equal(t, ver.ID, sess.ActiveVersionID)

	// Retrieve session
	fetchedSess, fetchedDoc, fetchedVer, err := svc.GetSession(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, fetchedSess.ID)
	assert.Equal(t, doc.ID, fetchedDoc.ID)
	assert.Equal(t, ver.ID, fetchedVer.ID)

	// Unauthorized guest
	wrongGuest := identity.Identity{
		ID:   "guest_wrong_" + uuid.New().String(),
		Type: identity.TypeGuest,
	}
	_, _, _, err = svc.GetSession(ctx, sess.ID, wrongGuest)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestService_ApplyOperation_SuccessAndIdempotency(t *testing.T) {
	svc := getTestService(t)
	ctx := context.Background()

	userUUID := uuid.New()
	userIdent := identity.Identity{
		ID:   userUUID.String(),
		Type: identity.TypeUser,
	}

	assetID := "ast_doc_" + uuid.New().String()
	initVDM := vdm.DocumentModel{
		DocumentID: "doc_vdm_init",
		PageCount:  1,
		Pages: []vdm.PageDescriptor{
			{
				PageID:           "p_page1",
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Rotation:         0,
			},
		},
	}

	doc, sess, v0, err := svc.CreateDocument(
		ctx,
		userIdent,
		"presentation.pdf",
		1024,
		1,
		assetID,
		"studio/sources/presentation.pdf",
		initVDM,
	)
	require.NoError(t, err)

	// 1. Apply Operation V1 (Rotate)
	rotatedVDM := initVDM
	rotatedVDM.Pages[0].Rotation = 90

	idemKey := "idem_rotate_" + uuid.New().String()
	req := ApplyOperationRequest{
		BaseVersionID:   v0.ID,
		IdempotencyKey:  idemKey,
		OperationName:   "rotate",
		Parameters:      json.RawMessage(`{"angle":90}`),
		TargetPageIDs:   []string{"p_page1"},
		NewVirtualModel: rotatedVDM,
		IsMaterialized:  false,
	}

	res1, err := svc.ApplyOperation(ctx, sess.ID, userIdent, req)
	require.NoError(t, err)
	assert.False(t, res1.IsIdempotentReplay)
	assert.Equal(t, 1, res1.Version.VersionNumber)
	assert.Equal(t, "rotate", res1.Version.OperationType)
	assert.Equal(t, &v0.ID, res1.Version.ParentVersionID)

	// 2. Re-apply same operation with same idempotency key (Idempotency Replay)
	res2, err := svc.ApplyOperation(ctx, sess.ID, userIdent, req)
	require.NoError(t, err)
	assert.True(t, res2.IsIdempotentReplay)
	assert.Equal(t, res1.Version.ID, res2.Version.ID)
	assert.Equal(t, res1.Operation.ID, res2.Operation.ID)

	// 3. Stale Base Version OCC Conflict
	staleReq := req
	staleReq.IdempotencyKey = "idem_new_" + uuid.New().String()
	staleReq.BaseVersionID = v0.ID // sess.ActiveVersionID is now res1.Version.ID (V1), not V0
	_, err = svc.ApplyOperation(ctx, sess.ID, userIdent, staleReq)
	assert.ErrorIs(t, err, ErrInvalidBaseVersion)

	_ = doc
}

func TestService_ConcurrentIdempotencyRace(t *testing.T) {
	svc := getTestService(t)
	ctx := context.Background()

	guestIdent := identity.Identity{
		ID:   "guest_concurrency_" + uuid.New().String(),
		Type: identity.TypeGuest,
	}

	assetID := "ast_race_" + uuid.New().String()
	initVDM := vdm.DocumentModel{
		DocumentID: "doc_race",
		PageCount:  1,
		Pages: []vdm.PageDescriptor{
			{
				PageID:           "p1",
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Rotation:         0,
			},
		},
	}

	_, sess, v0, err := svc.CreateDocument(
		ctx,
		guestIdent,
		"concurrency.pdf",
		1024,
		1,
		assetID,
		"studio/sources/concurrency.pdf",
		initVDM,
	)
	require.NoError(t, err)

	idemKey := "idem_concurrent_race_" + uuid.New().String()
	mutatedVDM := initVDM
	mutatedVDM.Pages[0].Rotation = 180

	req := ApplyOperationRequest{
		BaseVersionID:   v0.ID,
		IdempotencyKey:  idemKey,
		OperationName:   "rotate_180",
		Parameters:      json.RawMessage(`{"angle":180}`),
		TargetPageIDs:   []string{"p1"},
		NewVirtualModel: mutatedVDM,
		IsMaterialized:  false,
	}

	concurrency := 4
	var wg sync.WaitGroup
	results := make([]*ApplyOperationResult, concurrency)
	errors := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := svc.ApplyOperation(ctx, sess.ID, guestIdent, req)
			results[idx] = res
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	for i := 0; i < concurrency; i++ {
		require.NoError(t, errors[i])
		require.NotNil(t, results[i])
	}

	// All concurrent requests must resolve to the identical version ID
	winningVerID := results[0].Version.ID
	for i := 1; i < concurrency; i++ {
		assert.Equal(t, winningVerID, results[i].Version.ID)
		assert.Equal(t, results[0].Operation.ID, results[i].Operation.ID)
	}
}

func TestService_UndoRedoAndBranchCheckout(t *testing.T) {
	svc := getTestService(t)
	ctx := context.Background()

	guestIdent := identity.Identity{
		ID:   "guest_dag_" + uuid.New().String(),
		Type: identity.TypeGuest,
	}

	assetID := "ast_dag_" + uuid.New().String()
	vdm0 := vdm.DocumentModel{
		DocumentID: "doc_dag",
		PageCount:  1,
		Pages: []vdm.PageDescriptor{
			{PageID: "p1", SourceAssetID: &assetID, SourcePageNumber: 1, Rotation: 0},
		},
	}

	_, sess, v0, err := svc.CreateDocument(ctx, guestIdent, "dag.pdf", 1024, 1, assetID, "studio/sources/dag.pdf", vdm0)
	require.NoError(t, err)

	// Apply Op 1 -> V1
	vdm1 := vdm0
	vdm1.Pages[0].Rotation = 90
	res1, err := svc.ApplyOperation(ctx, sess.ID, guestIdent, ApplyOperationRequest{
		BaseVersionID:   v0.ID,
		IdempotencyKey:  "idem_v1_" + uuid.New().String(),
		OperationName:   "rotate_90",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm1,
	})
	require.NoError(t, err)
	v1 := res1.Version

	// Apply Op 2 -> V2
	vdm2 := vdm1
	vdm2.Pages[0].Rotation = 180
	res2, err := svc.ApplyOperation(ctx, sess.ID, guestIdent, ApplyOperationRequest{
		BaseVersionID:   v1.ID,
		IdempotencyKey:  "idem_v2_" + uuid.New().String(),
		OperationName:   "rotate_180",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm2,
	})
	require.NoError(t, err)
	v2 := res2.Version

	// 1. Undo from V2 -> V1
	undoVer1, err := svc.Undo(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, v1.ID, undoVer1.ID)

	// 2. Undo from V1 -> V0
	undoVer0, err := svc.Undo(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, v0.ID, undoVer0.ID)

	// Cannot undo further
	_, err = svc.Undo(ctx, sess.ID, guestIdent)
	assert.ErrorIs(t, err, ErrNoParentVersion)

	// 3. Redo from V0 -> V1
	redoVer1, err := svc.Redo(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, v1.ID, redoVer1.ID)

	// 4. Redo from V1 -> V2
	redoVer2, err := svc.Redo(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, v2.ID, redoVer2.ID)

	// Cannot redo further
	_, err = svc.Redo(ctx, sess.ID, guestIdent)
	assert.ErrorIs(t, err, ErrNoRedoChild)

	// 5. Create a branch: Undo to V1, apply new Op 3 -> V3
	_, err = svc.Undo(ctx, sess.ID, guestIdent)
	require.NoError(t, err)

	vdm3 := vdm1
	vdm3.Pages[0].Rotation = 270
	res3, err := svc.ApplyOperation(ctx, sess.ID, guestIdent, ApplyOperationRequest{
		BaseVersionID:   v1.ID,
		IdempotencyKey:  "idem_v3_" + uuid.New().String(),
		OperationName:   "rotate_270",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm3,
	})
	require.NoError(t, err)
	v3 := res3.Version

	// 6. Checkout back to alternative branch V2
	checkedOutVer, err := svc.CheckoutVersion(ctx, sess.ID, guestIdent, v2.ID)
	require.NoError(t, err)
	assert.Equal(t, v2.ID, checkedOutVer.ID)

	// History DAG contains V0, V1, V2, V3
	versions, ops, err := svc.GetVersionHistory(ctx, sess.ID, guestIdent)
	require.NoError(t, err)
	assert.Equal(t, 4, len(versions))
	assert.Equal(t, 3, len(ops))

	_ = v3
}

func TestService_RegisterSnapshotAndExport(t *testing.T) {
	svc := getTestService(t)
	ctx := context.Background()

	guestIdent := identity.Identity{
		ID:   "guest_snap_" + uuid.New().String(),
		Type: identity.TypeGuest,
	}

	assetID := "ast_snap_test_" + uuid.New().String()
	vdm0 := vdm.DocumentModel{
		DocumentID: "doc_snap",
		PageCount:  1,
		Pages: []vdm.PageDescriptor{
			{PageID: "p1", SourceAssetID: &assetID, SourcePageNumber: 1, Rotation: 0},
		},
	}

	doc, sess, v0, err := svc.CreateDocument(ctx, guestIdent, "snapshot.pdf", 1024, 1, assetID, "studio/sources/snapshot.pdf", vdm0)
	require.NoError(t, err)

	// Register Snapshot
	snapAssetID := "ast_snapshot_bin_" + uuid.New().String()
	snap, err := svc.RegisterSnapshot(ctx, doc.ID, v0.ID, snapAssetID, "studio/snapshots/v0.pdf", 2048, 1)
	require.NoError(t, err)
	assert.Equal(t, v0.ID, snap.VersionID)
	assert.Equal(t, snapAssetID, snap.AssetID)

	// Create Export
	export, err := svc.CreateExport(ctx, sess.ID, guestIdent, "pdf", "studio/exports/final.pdf", 2048, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, doc.ID, export.DocumentID)
	assert.Equal(t, v0.ID, export.VersionID)
	assert.Equal(t, "pdf", export.ExportFormat)
}
