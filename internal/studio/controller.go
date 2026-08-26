package studio

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/vdm"
	"pdfnest-backend/internal/uploads"
)

// Controller handles HTTP requests for Studio V2 endpoints.
type Controller struct {
	service      Service
	coordinator  OperationCoordinator
	renderer     TileRenderer
	finalizer    StudioFinalizer
	materializer StudioMaterializationCoordinator
}

// CreateSessionFromUpload initializes a Studio session from an actual PDF.
// It deliberately accepts no client-provided page metadata or VDM model.
func (ctrl *Controller) CreateSessionFromUpload(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	doc, sess, ver, err := ctrl.service.CreateDocumentFromSourceUpload(c.Context(), ident, SourceUploadInput{
		Path:         upload.Path,
		OriginalName: upload.Header.Filename,
		ContentType:  upload.Header.Header.Get("Content-Type"),
	})
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(ver.VirtualModel, &parsedVDM)
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"session":        sess,
		"document":       doc,
		"active_version": ver,
		"vdm":            parsedVDM,
	})
}

// CreateSecondaryAsset stores an additional PDF owned by the current Studio
// document. It intentionally creates neither a session nor a version.
func (ctrl *Controller) CreateSecondaryAsset(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	asset, err := ctrl.service.CreateSecondaryAsset(c.Context(), sessionID, ident, SourceUploadInput{
		Path:         upload.Path,
		OriginalName: upload.Header.Filename,
		ContentType:  upload.Header.Header.Get("Content-Type"),
	})
	if err != nil {
		return ctrl.mapError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"asset": asset})
}

// NewController initializes a new Studio V2 controller.
func NewController(service Service, coordinator OperationCoordinator, renderer TileRenderer, finalizer StudioFinalizer, materializer StudioMaterializationCoordinator) *Controller {
	return &Controller{
		service:      service,
		coordinator:  coordinator,
		renderer:     renderer,
		finalizer:    finalizer,
		materializer: materializer,
	}
}

// CreateSessionRequest encapsulates document creation parameters.
type CreateSessionRequest struct {
	FileName         string            `json:"file_name"`
	FileSize         int64             `json:"file_size"`
	InitialPageCount int               `json:"initial_page_count"`
	SourceAssetID    string            `json:"source_asset_id"`
	SourceR2Key      string            `json:"source_r2_key"`
	InitialVDM       vdm.DocumentModel `json:"initial_vdm"`
}

// CreateSession initializes a new Studio document and session.
func (ctrl *Controller) CreateSession(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)

	var req CreateSessionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request payload",
		})
	}

	if req.FileName == "" {
		req.FileName = "untitled.pdf"
	}
	if req.InitialPageCount <= 0 {
		req.InitialPageCount = len(req.InitialVDM.Pages)
		if req.InitialPageCount == 0 {
			req.InitialPageCount = 1
		}
	}
	if req.InitialVDM.DocumentID == "" {
		req.InitialVDM.DocumentID = uuid.New().String()
	}
	if len(req.InitialVDM.Pages) == 0 {
		pageID := uuid.New().String()
		req.InitialVDM.PageCount = 1
		req.InitialVDM.Pages = []vdm.PageDescriptor{
			{
				PageID:           pageID,
				SourceAssetID:    &req.SourceAssetID,
				SourcePageNumber: 1,
				Rotation:         0,
				IsBlank:          req.SourceAssetID == "",
			},
		}
	}

	doc, sess, ver, err := ctrl.service.CreateDocument(
		c.Context(),
		ident,
		req.FileName,
		req.FileSize,
		req.InitialPageCount,
		req.SourceAssetID,
		req.SourceR2Key,
		req.InitialVDM,
	)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(ver.VirtualModel, &parsedVDM)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"session":        sess,
		"document":       doc,
		"active_version": ver,
		"vdm":            parsedVDM,
	})
}

// GetSession retrieves the authoritative session and active document state.
func (ctrl *Controller) GetSession(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	sess, doc, ver, err := ctrl.service.GetSession(c.Context(), sessionID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(ver.VirtualModel, &parsedVDM)

	return c.JSON(fiber.Map{
		"session":        sess,
		"document":       doc,
		"active_version": ver,
		"vdm":            parsedVDM,
	})
}

// ApplyOperation dispatches a VDM transformation under optimistic concurrency control.
func (ctrl *Controller) ApplyOperation(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	var req ApplyOperationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid operation payload"})
	}

	res, err := ctrl.service.ApplyOperation(c.Context(), sessionID, ident, req)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(res.Version.VirtualModel, &parsedVDM)

	return c.JSON(fiber.Map{
		"version":              res.Version,
		"operation":            res.Operation,
		"is_idempotent_replay": res.IsIdempotentReplay,
		"vdm":                  parsedVDM,
	})
}

// ExecuteCommand applies a constrained, server-derived VDM mutation.
func (ctrl *Controller) ExecuteCommand(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	var req ExecuteCommandRequest
	if err := decodeStrictParameters(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid command payload"})
	}

	res, err := ctrl.coordinator.Execute(c.Context(), sessionID, ident, req)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(res.Version.VirtualModel, &parsedVDM)
	return c.JSON(fiber.Map{
		"version":              res.Version,
		"operation":            res.Operation,
		"is_idempotent_replay": res.IsIdempotentReplay,
		"vdm":                  parsedVDM,
	})
}

// Undo navigates the session active version to its parent version node.
func (ctrl *Controller) Undo(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	parentVer, err := ctrl.service.Undo(c.Context(), sessionID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(parentVer.VirtualModel, &parsedVDM)

	return c.JSON(fiber.Map{
		"version": parentVer,
		"vdm":     parsedVDM,
	})
}

// Redo navigates the session active version to its preferred child branch node.
func (ctrl *Controller) Redo(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	childVer, err := ctrl.service.Redo(c.Context(), sessionID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(childVer.VirtualModel, &parsedVDM)

	return c.JSON(fiber.Map{
		"version": childVer,
		"vdm":     parsedVDM,
	})
}

// GetHistory retrieves the full immutable version DAG and operation list for the document.
func (ctrl *Controller) GetHistory(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	versions, operations, err := ctrl.service.GetVersionHistory(c.Context(), sessionID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"versions":   versions,
		"operations": operations,
	})
}

// CheckoutRequest holds the target version ID for branching or rollback.
type CheckoutRequest struct {
	TargetVersionID uuid.UUID `json:"target_version_id"`
}

// Checkout switches the active session version to any historical DAG node.
func (ctrl *Controller) Checkout(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionIDStr := c.Params("id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	var req CheckoutRequest
	if err := c.BodyParser(&req); err != nil || req.TargetVersionID == uuid.Nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "target_version_id is required"})
	}

	targetVer, err := ctrl.service.CheckoutVersion(c.Context(), sessionID, ident, req.TargetVersionID)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	var parsedVDM interface{}
	_ = json.Unmarshal(targetVer.VirtualModel, &parsedVDM)

	return c.JSON(fiber.Map{
		"version": targetVer,
		"vdm":     parsedVDM,
	})
}

// FinalizeExport materializes the session's currently active VDM and returns
// a secure, session-scoped download reference. The controller stays thin: all
// source resolution, composition, validation, and persistence live in the
// StudioFinalizer boundary.
func (ctrl *Controller) FinalizeExport(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}
	if ctrl.finalizer == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "studio finalizer is not configured"})
	}

	result, err := ctrl.finalizer.Finalize(c.Context(), sessionID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}
	return c.JSON(fiber.Map{
		"export":        result.Export,
		"file_name":     result.FileName,
		"download_path": "/api/studio/v1/sessions/" + sessionID.String() + "/exports/" + result.Export.ID.String() + "/download",
	})
}

// DownloadExport streams an owned, unexpired Studio export as an attachment.
func (ctrl *Controller) DownloadExport(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}
	exportID, err := uuid.Parse(c.Params("export_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid export id"})
	}
	if ctrl.finalizer == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "studio finalizer is not configured"})
	}

	download, err := ctrl.finalizer.ResolveDownload(c.Context(), sessionID, exportID, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}
	defer download.Cleanup()
	return c.Download(download.Path, download.FileName)
}

// Materialize creates a new immutable materialized Studio version from the
// active version. It intentionally has a separate route from Export because
// internal processing must not create a user-visible StudioExport record.
func (ctrl *Controller) Materialize(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)
	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}
	if ctrl.materializer == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "studio materialization coordinator is not configured"})
	}
	var req MaterializationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid materialization request"})
	}
	result, err := ctrl.materializer.Execute(c.Context(), sessionID, ident, req)
	if err != nil {
		return ctrl.mapError(c, err)
	}
	return c.JSON(result)
}

// GetPageTile renders and streams a viewport-aware JPEG tile for a specific page and version.
func (ctrl *Controller) GetPageTile(c *fiber.Ctx) error {
	ident := identity.MustFromContext(c)

	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	versionID, err := uuid.Parse(c.Params("version_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid version id"})
	}

	pageID := c.Params("page_id")
	if pageID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "page_id is required"})
	}

	scaleStr := c.Query("scale", "1.5")
	scale, err := strconv.ParseFloat(scaleStr, 64)
	if err != nil || scale <= 0 {
		scale = 1.5
	}

	tileX, _ := strconv.Atoi(c.Query("tile_x", "0"))
	tileY, _ := strconv.Atoi(c.Query("tile_y", "0"))
	tileW, _ := strconv.Atoi(c.Query("tile_w", "0"))
	tileH, _ := strconv.Atoi(c.Query("tile_h", "0"))

	req := TileRequest{
		SessionID: sessionID,
		VersionID: versionID,
		PageID:    pageID,
		Scale:     scale,
		TileX:     tileX,
		TileY:     tileY,
		TileW:     tileW,
		TileH:     tileH,
	}

	imageBytes, err := ctrl.renderer.RenderTile(c.Context(), req, ident)
	if err != nil {
		return ctrl.mapError(c, err)
	}

	c.Set("Content-Type", "image/jpeg")
	c.Set("Content-Length", strconv.Itoa(len(imageBytes)))
	c.Set("Cache-Control", "private, max-age=300")
	return c.Send(imageBytes)
}

// GetMetrics returns observability telemetry for tile rendering and cache.
func (ctrl *Controller) GetMetrics(c *fiber.Ctx) error {
	metrics := ctrl.renderer.GetMetrics()
	return c.JSON(metrics)
}

func (ctrl *Controller) mapError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, ErrDocumentNotFound), errors.Is(err, ErrSessionNotFound), errors.Is(err, ErrVersionNotFound), errors.Is(err, ErrPageNotFound), errors.Is(err, ErrAssetNotFound), errors.Is(err, ErrExportNotFound):
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrUnauthorized):
		return c.Status(http.StatusForbidden).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrSessionExpired):
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrExportExpired):
		return c.Status(http.StatusGone).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrInvalidBaseVersion), errors.Is(err, ErrConflict), errors.Is(err, ErrIdempotencyConflict):
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrWorkerBusy):
		c.Set("Retry-After", "2")
		return c.Status(http.StatusTooManyRequests).JSON(fiber.Map{"error": err.Error(), "code": "RENDER_WORKER_BUSY"})
	case errors.Is(err, ErrRenderTimeout):
		return c.Status(http.StatusGatewayTimeout).JSON(fiber.Map{"error": err.Error(), "code": "RENDER_TIMEOUT"})
	case errors.Is(err, ErrNoParentVersion), errors.Is(err, ErrNoRedoChild), errors.Is(err, ErrInvalidBranchTarget), errors.Is(err, ErrInvalidOperation), errors.Is(err, ErrUnknownCommand), errors.Is(err, ErrInvalidCommand), errors.Is(err, ErrCommandPageNotFound), errors.Is(err, ErrDuplicatePageID), errors.Is(err, ErrInvalidPageOrder), errors.Is(err, ErrCannotDeleteAll), errors.Is(err, ErrBlankDimensions), errors.Is(err, ErrInvalidCropBox), errors.Is(err, ErrInvalidMetadata), errors.Is(err, ErrInvalidTileCoords), errors.Is(err, ErrInvalidTileScale), errors.Is(err, ErrTileTooLarge), errors.Is(err, ErrVersionMismatch), errors.Is(err, ErrInvalidMaterialization), errors.Is(err, ErrMaterializationProcessorUnavailable):
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrInvalidSourcePDF), errors.Is(err, ErrEncryptedSourcePDF), errors.Is(err, ErrSourcePageLimit):
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, ErrSourceTooLarge):
		return c.Status(http.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": err.Error()})
	default:
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
}
