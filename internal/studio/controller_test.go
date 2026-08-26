package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

func setupTestApp(t *testing.T) (*fiber.App, Repository, Service, TileRenderer, *gorm.DB) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to PostgreSQL")

	err = db.AutoMigrate(
		&models.StudioDocument{},
		&models.StudioAsset{},
		&models.StudioSnapshot{},
		&models.StudioVersion{},
		&models.StudioOperation{},
		&models.StudioSession{},
		&models.StudioExport{},
	)
	require.NoError(t, err)

	repo := NewRepository(db)
	service := NewService(repo)
	coordinator := NewOperationCoordinator(repo)
	renderer := NewTileRenderer(repo)
	controller := NewController(service, coordinator, renderer)

	app := fiber.New()

	// Mock identity middleware setting guest identity
	app.Use(func(c *fiber.Ctx) error {
		guestID := c.Get("X-Test-Guest-ID")
		if guestID == "" {
			guestID = "test_guest_default"
		}
		c.Locals(identity.LocalIdentityKey, identity.Identity{
			ID:   guestID,
			Type: identity.TypeGuest,
		})
		return c.Next()
	})

	RegisterRoutes(app.Group("/api"), controller)

	return app, repo, service, renderer, db
}

func TestController_ExecuteCommandRejectsCallerSuppliedVDM(t *testing.T) {
	app, _, service, _, _ := setupTestApp(t)
	guestID := "command_http_guest_" + uuid.NewString()
	ident := identity.Identity{ID: guestID, Type: identity.TypeGuest}
	assetID := "ast_http_command_" + uuid.NewString()
	pageID := "page_http_" + uuid.NewString()
	initial := vdm.DocumentModel{
		DocumentID: "doc_http_command_" + uuid.NewString(),
		PageCount:  1,
		Pages: []vdm.PageDescriptor{{
			PageID: pageID, SourceAssetID: &assetID, SourcePageNumber: 1,
			Dimensions: &vdm.Dimensions{Width: 612, Height: 792}, Rotation: 0, Overlays: []vdm.Overlay{},
		}},
	}
	_, session, version, err := service.CreateDocument(
		context.Background(), ident, "typed-command.pdf", 1024, 1, assetID,
		"studio/sources/"+uuid.NewString()+".pdf", initial,
	)
	require.NoError(t, err)

	malicious := map[string]interface{}{
		"base_version_id": version.ID,
		"idempotency_key": "smuggle_" + uuid.NewString(),
		"operation":       CommandRotatePage,
		"parameters": map[string]interface{}{
			"page_ids": []string{pageID}, "delta_degrees": 90,
		},
		"new_virtual_model": map[string]interface{}{
			"document_id": initial.DocumentID, "page_count": 0, "pages": []interface{}{},
		},
	}
	maliciousBody, err := json.Marshal(malicious)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+session.ID.String()+"/commands", bytes.NewReader(maliciousBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	legitimate := commandRequest(t, version.ID, "rotate_http_"+uuid.NewString(), CommandRotatePage, RotatePageParameters{
		PageIDs: []string{pageID}, DeltaDegrees: 90,
	})
	legitimateBody, err := json.Marshal(legitimate)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+session.ID.String()+"/commands", bytes.NewReader(legitimateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		VDM vdm.DocumentModel `json:"vdm"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Len(t, payload.VDM.Pages, 1)
	assert.Equal(t, 90, payload.VDM.Pages[0].Rotation)
}

func TestController_CreateSessionFromUpload_DerivesRealPDFState(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	app, repo, _, _, _ := setupTestApp(t)
	guestID := "guest_upload_" + uuid.NewString()
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "rendering", "corpus", "standard_a4_10p.pdf")
	fixture, err := os.Open(fixturePath)
	require.NoError(t, err, "real ten-page fixture must be available")
	defer fixture.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "standard_a4_10p.pdf")
	require.NoError(t, err)
	_, err = io.Copy(part, fixture)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/from-upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 10_000)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created struct {
		Session struct {
			ID              string `json:"id"`
			ActiveVersionID string `json:"active_version_id"`
		} `json:"session"`
		Document struct {
			ID               string `json:"id"`
			OriginalFileName string `json:"original_filename"`
			InitialPageCount int    `json:"initial_page_count"`
		} `json:"document"`
		ActiveVersion struct {
			ID             string `json:"id"`
			VersionNumber  int    `json:"version_number"`
			OperationType  string `json:"operation_type"`
			IsMaterialized bool   `json:"is_materialized"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	assert.Equal(t, "standard_a4_10p.pdf", created.Document.OriginalFileName)
	assert.Equal(t, 10, created.Document.InitialPageCount)
	assert.Equal(t, 0, created.ActiveVersion.VersionNumber)
	assert.Equal(t, "initial_upload", created.ActiveVersion.OperationType)
	assert.True(t, created.ActiveVersion.IsMaterialized)
	assert.Equal(t, created.Session.ActiveVersionID, created.ActiveVersion.ID)
	assert.Equal(t, 10, created.VDM.PageCount)
	require.Len(t, created.VDM.Pages, 10)

	assetID := *created.VDM.Pages[0].SourceAssetID
	for index, page := range created.VDM.Pages {
		require.NotNil(t, page.SourceAssetID)
		assert.Equal(t, assetID, *page.SourceAssetID)
		assert.Equal(t, index+1, page.SourcePageNumber)
		assert.False(t, page.IsBlank)
		assert.Equal(t, 0, page.Rotation)
		require.NotNil(t, page.Dimensions)
		assert.Greater(t, page.Dimensions.Width, 0.0)
		assert.Greater(t, page.Dimensions.Height, 0.0)
	}

	asset, err := repo.GetAsset(context.Background(), assetID)
	require.NoError(t, err)
	assert.Equal(t, created.Document.ID, asset.DocumentID.String())
	assert.Equal(t, "source_pdf", asset.AssetType)
	assert.Equal(t, "application/pdf", asset.MimeType)
	assert.True(t, storage.ObjectExists(context.Background(), asset.R2Key))

	// The initialized session is bound to the guest that performed the upload.
	getReq := httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+created.Session.ID, nil)
	getReq.Header.Set("X-Test-Guest-ID", guestID)
	getResp, err := app.Test(getReq, 5_000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	getResp.Body.Close()

	otherReq := httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+created.Session.ID, nil)
	otherReq.Header.Set("X-Test-Guest-ID", "other_"+uuid.NewString())
	otherResp, err := app.Test(otherReq, 5_000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, otherResp.StatusCode)
	otherResp.Body.Close()
}

func TestController_CompleteLifecycle(t *testing.T) {
	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_ctrl_" + uuid.New().String()

	// 1. Create Session (POST /api/studio/v1/sessions)
	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "audit_contract.pdf",
		FileSize:         1024,
		InitialPageCount: 1,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)

	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var createResp struct {
		Session struct {
			ID              string `json:"id"`
			DocumentID      string `json:"document_id"`
			ActiveVersionID string `json:"active_version_id"`
		} `json:"session"`
		Document struct {
			ID string `json:"id"`
		} `json:"document"`
		ActiveVersion struct {
			ID            string `json:"id"`
			VersionNumber int    `json:"version_number"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	err = json.NewDecoder(resp.Body).Decode(&createResp)
	require.NoError(t, err)

	sessionID := createResp.Session.ID
	v0ID := createResp.ActiveVersion.ID
	require.NotEmpty(t, sessionID)
	assert.Equal(t, 0, createResp.ActiveVersion.VersionNumber)

	// 2. Get Session (GET /api/studio/v1/sessions/:id)
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 3. Apply Operation -> V1 (POST /api/studio/v1/sessions/:id/operations)
	vdm1 := createResp.VDM
	vdm1.Pages[0].Rotation = 90

	baseUUID, _ := uuid.Parse(v0ID)
	opReqBody, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   baseUUID,
		IdempotencyKey:  "idem_ctrl_1_" + uuid.New().String(),
		OperationName:   "rotate",
		Parameters:      json.RawMessage(`{"angle":90}`),
		TargetPageIDs:   []string{vdm1.Pages[0].PageID},
		NewVirtualModel: vdm1,
	})

	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(opReqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var opResp struct {
		Version struct {
			ID            string `json:"id"`
			VersionNumber int    `json:"version_number"`
		} `json:"version"`
		IsIdempotentReplay bool `json:"is_idempotent_replay"`
	}
	err = json.NewDecoder(resp.Body).Decode(&opResp)
	require.NoError(t, err)
	v1ID := opResp.Version.ID
	assert.Equal(t, 1, opResp.Version.VersionNumber)
	assert.False(t, opResp.IsIdempotentReplay)

	// 4. Test Undo (POST /api/studio/v1/sessions/:id/undo) -> Back to V0
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/undo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var undoResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	err = json.NewDecoder(resp.Body).Decode(&undoResp)
	require.NoError(t, err)
	assert.Equal(t, v0ID, undoResp.Version.ID)

	// 5. Test Redo (POST /api/studio/v1/sessions/:id/redo) -> Forward to V1
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/redo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var redoResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	err = json.NewDecoder(resp.Body).Decode(&redoResp)
	require.NoError(t, err)
	assert.Equal(t, v1ID, redoResp.Version.ID)

	// 6. Test History (GET /api/studio/v1/sessions/:id/history)
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID+"/history", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var histResp struct {
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
		Operations []struct {
			ID string `json:"id"`
		} `json:"operations"`
	}
	err = json.NewDecoder(resp.Body).Decode(&histResp)
	require.NoError(t, err)
	assert.Equal(t, 2, len(histResp.Versions)) // V0 and V1
	assert.Equal(t, 1, len(histResp.Operations))

	// 7. Test Historical Checkout (POST /api/studio/v1/sessions/:id/checkout) -> Checkout V0
	checkoutBody, _ := json.Marshal(CheckoutRequest{
		TargetVersionID: baseUUID,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/checkout", bytes.NewReader(checkoutBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var checkoutResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	err = json.NewDecoder(resp.Body).Decode(&checkoutResp)
	require.NoError(t, err)
	assert.Equal(t, v0ID, checkoutResp.Version.ID)

	// 8. Test Unauthorized Access (Wrong guest token) -> 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID, nil)
	req.Header.Set("X-Test-Guest-ID", "wrong_attacker_guest")
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestController_VersionDAGBranchingAndLineage(t *testing.T) {
	app, _, _, _, db := setupTestApp(t)
	guestID := "guest_dag_branch_" + uuid.New().String()

	// 1. Create Session -> V0
	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName: "branching_test.pdf",
		FileSize: 2048,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0UUID, _ := uuid.Parse(initResp.ActiveVersion.ID)

	// 2. Op 1: V0 -> V1 (Rotate 90)
	vdm1 := initResp.VDM
	vdm1.Pages[0].Rotation = 90
	op1Body, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   v0UUID,
		IdempotencyKey:  "idem_dag_op1_" + uuid.New().String(),
		OperationName:   "rotate_90",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm1,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(op1Body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var op1Resp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&op1Resp)
	v1UUID, _ := uuid.Parse(op1Resp.Version.ID)

	// 3. Op 2: V1 -> V2 (Rotate 180)
	vdm2 := vdm1
	vdm2.Pages[0].Rotation = 180
	op2Body, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   v1UUID,
		IdempotencyKey:  "idem_dag_op2_" + uuid.New().String(),
		OperationName:   "rotate_180",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm2,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(op2Body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var op2Resp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&op2Resp)
	v2UUID, _ := uuid.Parse(op2Resp.Version.ID)

	// 4. Checkout V1 (Navigates active pointer to V1)
	checkoutBody, _ := json.Marshal(CheckoutRequest{TargetVersionID: v1UUID})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/checkout", bytes.NewReader(checkoutBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 5. Op 3: Create Branch from V1 -> V3 (Rotate 270)
	vdm3 := vdm1
	vdm3.Pages[0].Rotation = 270
	op3Body, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   v1UUID,
		IdempotencyKey:  "idem_dag_op3_" + uuid.New().String(),
		OperationName:   "rotate_270",
		Parameters:      json.RawMessage(`{}`),
		NewVirtualModel: vdm3,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(op3Body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var op3Resp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&op3Resp)
	v3UUID, _ := uuid.Parse(op3Resp.Version.ID)

	// --- Comprehensive Database & DAG Invariant Verification ---

	// A. Check that V0, V1, V2, V3 all exist immutably in DB
	var dbVersions []models.StudioVersion
	err = db.Where("id IN ?", []uuid.UUID{v0UUID, v1UUID, v2UUID, v3UUID}).Find(&dbVersions).Error
	require.NoError(t, err)
	assert.Equal(t, 4, len(dbVersions), "All 4 version nodes must exist immutably in DB")

	// B. Verify V3 parent is V1
	var dbV3 models.StudioVersion
	_ = db.First(&dbV3, "id = ?", v3UUID)
	require.NotNil(t, dbV3.ParentVersionID)
	assert.Equal(t, v1UUID, *dbV3.ParentVersionID)

	// C. Verify V1's preferred child is updated to the newly branched V3
	var dbV1 models.StudioVersion
	_ = db.First(&dbV1, "id = ?", v1UUID)
	require.NotNil(t, dbV1.PreferredChildID)
	assert.Equal(t, v3UUID, *dbV1.PreferredChildID)

	// D. Verify session active version is currently V3
	var sess models.StudioSession
	_ = db.First(&sess, "id = ?", sessionID)
	assert.Equal(t, v3UUID, sess.ActiveVersionID)

	// E. Undo from V3 returns to V1
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/undo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var undoResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&undoResp)
	assert.Equal(t, v1UUID.String(), undoResp.Version.ID)
	assert.Equal(t, 90, undoResp.VDM.Pages[0].Rotation)

	// F. Redo from V1 advances along preferred child to V3 (not V2)
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/redo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var redoResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&redoResp)
	assert.Equal(t, v3UUID.String(), redoResp.Version.ID)
	assert.Equal(t, 270, redoResp.VDM.Pages[0].Rotation)

	// G. History endpoint returns all 4 versions and 3 operations
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID+"/history", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var histResp struct {
		Versions   []models.StudioVersion   `json:"versions"`
		Operations []models.StudioOperation `json:"operations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&histResp)
	assert.Equal(t, 4, len(histResp.Versions))
	assert.Equal(t, 3, len(histResp.Operations))

	_ = v2UUID
}

func TestController_ErrorContracts(t *testing.T) {
	app, _, _, _, db := setupTestApp(t)
	guestID := "guest_error_contracts_" + uuid.New().String()

	// Create Session -> V0
	createBody, _ := json.Marshal(CreateSessionRequest{FileName: "errors.pdf"})
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID

	// 1. Nonexistent session -> 404
	nonExistentUUID := uuid.New().String()
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+nonExistentUUID, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 2. Wrong guest -> 403
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID, nil)
	req.Header.Set("X-Test-Guest-ID", "attacker_guest_token")
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 3. Invalid UUID format -> 400
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/not-a-uuid", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 4. Undo at root (V0 has no parent) -> 400
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/undo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 5. Redo when no child branch exists -> 400
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/redo", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 6. Checkout nonexistent version target -> 404
	checkoutNonExistent, _ := json.Marshal(CheckoutRequest{TargetVersionID: uuid.New()})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/checkout", bytes.NewReader(checkoutNonExistent))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 7. Stale ApplyOperation base_version_id -> 409 Conflict
	staleOpBody, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:  uuid.New(), // Mismatched base version
		IdempotencyKey: "idem_stale_" + uuid.New().String(),
		OperationName:  "rotate",
		NewVirtualModel: vdm.DocumentModel{
			DocumentID: "doc_err",
			PageCount:  1,
			Pages: []vdm.PageDescriptor{
				{PageID: "p1", IsBlank: true},
			},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(staleOpBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// 8. Expired session -> 401 Unauthorized
	// Manually expire session in DB
	err := db.Model(&models.StudioSession{}).Where("id = ?", sessionID).Update("expires_at", time.Now().UTC().Add(-1*time.Hour)).Error
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/sessions/"+sessionID, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestController_IdempotencyReplay(t *testing.T) {
	app, _, _, _, db := setupTestApp(t)
	guestID := "guest_idem_test_" + uuid.New().String()

	// 1. Create Session
	createBody, _ := json.Marshal(CreateSessionRequest{FileName: "idem.pdf"})
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	var initResp struct {
		Session struct {
			ID         string `json:"id"`
			DocumentID string `json:"document_id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0UUID, _ := uuid.Parse(initResp.ActiveVersion.ID)
	docUUID, _ := uuid.Parse(initResp.Session.DocumentID)

	vdmMutated := initResp.VDM
	vdmMutated.Pages[0].Rotation = 90
	idemKey := "idem_key_exact_replay_" + uuid.New().String()

	opBody, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   v0UUID,
		IdempotencyKey:  idemKey,
		OperationName:   "rotate_90",
		Parameters:      json.RawMessage(`{"angle":90}`),
		NewVirtualModel: vdmMutated,
	})

	// Dispatch Request 1
	req1 := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(opBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Test-Guest-ID", guestID)
	resp1, err := app.Test(req1, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	var res1 struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
		IsIdempotentReplay bool `json:"is_idempotent_replay"`
	}
	_ = json.NewDecoder(resp1.Body).Decode(&res1)
	assert.False(t, res1.IsIdempotentReplay, "First request must NOT be a replay")
	winningVerID := res1.Version.ID

	// Dispatch Request 2 (Exact duplicate idempotency key)
	req2 := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(opBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Test-Guest-ID", guestID)
	resp2, err := app.Test(req2, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	var res2 struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
		IsIdempotentReplay bool `json:"is_idempotent_replay"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&res2)
	assert.True(t, res2.IsIdempotentReplay, "Second request MUST report idempotent replay")
	assert.Equal(t, winningVerID, res2.Version.ID, "Replayed response must return exact same version ID")

	// Verify database record count
	var opCount int64
	db.Model(&models.StudioOperation{}).Where("document_id = ? AND idempotency_key = ?", docUUID, idemKey).Count(&opCount)
	assert.Equal(t, int64(1), opCount, "Exactly 1 operation record must exist in PostgreSQL")

	var verCount int64
	db.Model(&models.StudioVersion{}).Where("document_id = ?", docUUID).Count(&verCount)
	assert.Equal(t, int64(2), verCount, "Exactly 2 version records (V0 and V1) must exist in PostgreSQL")
}

func TestController_TilePreview_Comprehensive(t *testing.T) {
	app, _, _, _, db := setupTestApp(t)
	guestID := "guest_tile_test_" + uuid.New().String()

	// 1. Create Session
	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName: "tile_test.pdf",
		FileSize: 1024,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID
	pageID := initResp.VDM.Pages[0].PageID

	// 2. Request Valid Tile (Full Page)
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.5", sessionID, v0ID, pageID)
	req = httptest.NewRequest(http.MethodGet, tileURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))

	bodyBytes := make([]byte, resp.ContentLength)
	_, _ = resp.Body.Read(bodyBytes)
	assert.True(t, len(bodyBytes) > 100, "Tile response must contain valid JPEG payload")
	assert.Equal(t, byte(0xFF), bodyBytes[0], "Must start with JPEG magic byte 0xFF")
	assert.Equal(t, byte(0xD8), bodyBytes[1], "Must start with JPEG magic byte 0xD8")

	// 3. Request Sub-Tile with Coordinates
	subTileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.5&tile_x=0&tile_y=0&tile_w=200&tile_h=200", sessionID, v0ID, pageID)
	req = httptest.NewRequest(http.MethodGet, subTileURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 4. Test Cache Hit on Sub-Tile
	req = httptest.NewRequest(http.MethodGet, subTileURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Check metrics
	req = httptest.NewRequest(http.MethodGet, "/api/studio/v1/metrics", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err = app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var metrics TileMetrics
	_ = json.NewDecoder(resp.Body).Decode(&metrics)
	assert.True(t, metrics.CacheHits >= 1, "Cache hit must be recorded")

	// 5. Error Test: Nonexistent Page -> 404
	invalidPageURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile", sessionID, v0ID, "nonexistent-page-id")
	req = httptest.NewRequest(http.MethodGet, invalidPageURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 6. Error Test: Invalid Scale -> 400
	invalidScaleURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=99.0", sessionID, v0ID, pageID)
	req = httptest.NewRequest(http.MethodGet, invalidScaleURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 7. Error Test: Tile Coordinates Exceeding Boundaries -> 400
	oobTileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=5000&tile_y=5000&tile_w=500&tile_h=500", sessionID, v0ID, pageID)
	req = httptest.NewRequest(http.MethodGet, oobTileURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 8. Error Test: Unauthorized Access -> 403
	req = httptest.NewRequest(http.MethodGet, tileURL, nil)
	req.Header.Set("X-Test-Guest-ID", "attacker_guest_token")
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 9. Version Isolation Test: Compare Tile Bytes across Rotated Versions
	vdm1 := initResp.VDM
	vdm1.Pages[0].Rotation = 90
	baseUUID, _ := uuid.Parse(v0ID)
	opBody, _ := json.Marshal(ApplyOperationRequest{
		BaseVersionID:   baseUUID,
		IdempotencyKey:  "idem_tile_rot_" + uuid.New().String(),
		OperationName:   "rotate_90",
		NewVirtualModel: vdm1,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/"+sessionID+"/operations", bytes.NewReader(opBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var opResp struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&opResp)
	v1ID := opResp.Version.ID

	v1TileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.5", sessionID, v1ID, pageID)
	req = httptest.NewRequest(http.MethodGet, v1TileURL, nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ = app.Test(req, 5000)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	_ = db
}
