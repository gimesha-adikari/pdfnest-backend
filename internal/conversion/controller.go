package conversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/disk"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/temp"
	"pdfnest-backend/internal/uploads"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

type CanvasLayoutItem struct {
	ID          string  `json:"id"`
	FileIndex   int     `json:"fileIndex"`
	Name        string  `json:"name"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	BorderWidth float64 `json:"borderWidth"`
	BorderColor string  `json:"borderColor"`
	ZIndex      int     `json:"zIndex"`
	PageIndex   int     `json:"pageIndex"`
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) ConvertImagesToPDF(c *fiber.Ctx) error {
	files, err := uploads.MustFiles(c, "images")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one image file is required for compilation.",
		})
	}

	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one image file is required for compilation.",
		})
	}

	temporaryImagePaths := make([]string, 0, len(files))
	for _, file := range files {
		temporaryImagePaths = append(temporaryImagePaths, file.Path)
	}

	outputPath, err := ctrl.service.ImagesToPDF(temporaryImagePaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "PDF_COMPILATION_FAILED",
			Message: "Image matrix processing pipeline failure: " + err.Error(),
		})
	}
	defer func() {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to purge temporary output compiled PDF at %s: %v", outputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_images.pdf")
	return c.SendFile(outputPath)
}

func (ctrl *Controller) RasterizePdfUniversal(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF file upload parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_RASTERIZE", 300); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	requiredBytes := disk.EstimateRequiredSpace(upload.Header.Size, 15.0, 100*1024*1024)
	if diskErr := disk.CheckAvailableSpace(temp.GetDir(), requiredBytes); diskErr != nil {
		return c.Status(fiber.StatusInsufficientStorage).JSON(APIError{
			Code:    "INSUFFICIENT_STORAGE",
			Message: "Insufficient server disk space available to perform PDF rasterization operation.",
		})
	}

	imageType := c.FormValue("image_type", "jpg")

	zipOutputPath, err := ctrl.service.PdfToImagesBackend(c.UserContext(), upload.Path, imageType)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "RASTERIZATION_FAILED",
			Message: "PDF extraction routine runtime failure: " + err.Error(),
		})
	}
	defer func() {
		if cleanupErr := os.Remove(zipOutputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary output ZIP file archive at %s: %v", zipOutputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/zip")
	c.Attachment("extracted_pages.zip")
	return c.SendFile(zipOutputPath)
}

func (cc *Controller) StreamPagePreviewHandler(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"code":    "MISSING_FILE",
			"message": "Payload validation schema rejected: file is required.",
		})
	}

	pageStr := c.FormValue("page", "1")
	pageNum, err := strconv.Atoi(pageStr)
	if err != nil || pageNum < 1 {
		pageNum = 1
	}

	scaleStr := c.FormValue("scale", "2.0")
	scale, err := strconv.ParseFloat(scaleStr, 64)
	if err != nil || scale <= 0 {
		scale = 2.0
	}

	ownerID := previewOwnerID(c)
	if ownerID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"code":    "PREVIEW_IDENTITY_REQUIRED",
			"message": "A preview identity is required.",
		})
	}

	imgBytes, err := cc.service.ConvertPageToImageStream(c.UserContext(), upload.Header, pageNum, scale, ownerID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{
			"code":    "RASTER_ENGINE_CRASH",
			"message": err.Error(),
		})
	}

	c.Set("Content-Type", "image/jpeg")
	c.Set("Content-Length", strconv.Itoa(length(imgBytes)))
	c.Set("Cache-Control", "private, max-age=60")

	return c.Send(imgBytes)
}

func (cc *Controller) CreatePreviewSessionHandler(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"code":    "MISSING_FILE",
			"message": "Payload validation schema rejected: file is required.",
		})
	}

	ownerID := previewOwnerID(c)
	if ownerID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"code":    "PREVIEW_IDENTITY_REQUIRED",
			"message": "A preview identity is required.",
		})
	}

	session, err := cc.service.CreatePreviewSession(
		c.UserContext(),
		upload.Header,
		ownerID,
	)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{
			"code":    "PREVIEW_SESSION_CREATE_FAILED",
			"message": err.Error(),
		})
	}

	return c.Status(200).JSON(session)
}
func (cc *Controller) StreamPreviewSessionPageHandler(c *fiber.Ctx) error {
	sessionID := c.Params("sessionId")
	if sessionID == "" {
		return c.Status(400).JSON(fiber.Map{
			"code":    "MISSING_SESSION_ID",
			"message": "Preview session ID is required.",
		})
	}

	pageStr := c.Params("page")
	pageNum, err := strconv.Atoi(pageStr)
	if err != nil || pageNum < 1 {
		return c.Status(400).JSON(fiber.Map{
			"code":    "INVALID_PAGE",
			"message": "Page number must be greater than 0.",
		})
	}

	scaleStr := c.Query("scale", "2.0")
	scale, err := strconv.ParseFloat(scaleStr, 64)
	if err != nil || scale <= 0 {
		scale = 2.0
	}

	ownerID := previewOwnerID(c)
	if ownerID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"code":    "PREVIEW_IDENTITY_REQUIRED",
			"message": "A preview identity is required.",
		})
	}

	imgBytes, err := cc.service.GetPreviewSessionPage(
		c.UserContext(),
		sessionID,
		pageNum,
		scale,
		ownerID,
	)
	if err != nil {
		if errors.Is(err, ErrPreviewSessionForbidden) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"code":    "PREVIEW_SESSION_FORBIDDEN",
				"message": "This preview is not available to this identity.",
			})
		}
		if errors.Is(err, ErrPreviewSessionNotFound) {
			return c.Status(404).JSON(fiber.Map{
				"code":    "SESSION_NOT_FOUND",
				"message": "This preview session is no longer available.",
			})
		}

		return c.Status(500).JSON(fiber.Map{
			"code":    "PREVIEW_SESSION_PAGE_FAILED",
			"message": err.Error(),
		})
	}

	c.Set("Content-Type", "image/jpeg")
	c.Set(
		"Content-Length",
		strconv.Itoa(len(imgBytes)),
	)
	c.Set(
		"Cache-Control",
		"private, max-age=60",
	)

	return c.Send(imgBytes)
}

func (cc *Controller) DeletePreviewSessionHandler(c *fiber.Ctx) error {
	sessionID := c.Params("sessionId")
	if sessionID == "" {
		return c.Status(400).JSON(fiber.Map{
			"code":    "MISSING_SESSION_ID",
			"message": "Preview session ID is required.",
		})
	}

	ownerID := previewOwnerID(c)
	if ownerID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"code":    "PREVIEW_IDENTITY_REQUIRED",
			"message": "A preview identity is required.",
		})
	}

	if err := cc.service.DeletePreviewSession(
		c.UserContext(),
		sessionID,
		ownerID,
	); err != nil {
		if errors.Is(err, ErrPreviewSessionForbidden) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"code":    "PREVIEW_SESSION_FORBIDDEN",
				"message": "This preview is not available to this identity.",
			})
		}
		return c.Status(500).JSON(fiber.Map{
			"code":    "PREVIEW_SESSION_DELETE_FAILED",
			"message": err.Error(),
		})
	}

	return c.SendStatus(204)
}

func previewOwnerID(c *fiber.Ctx) string {
	ownerID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	return strings.TrimSpace(ownerID)
}

func length(b []byte) int {
	return len(b)
}

func (ctrl *Controller) ConvertOfficeToPDF(c *fiber.Ctx) error {
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source document upload asset parameter.",
		})
	}

	outputPath, err := ctrl.service.OfficeToPdf(c.UserContext(), upload.Path)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OFFICE_CONVERSION_FAILED",
			Message: "Office calculation matrix routine failed: " + err.Error(),
		})
	}
	defer func() { _ = os.Remove(outputPath) }()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("converted_office_doc.pdf")
	return c.SendFile(outputPath)
}

func (ctrl *Controller) ConvertUrlToPDF(c *fiber.Ctx) error {
	targetURL := c.FormValue("url")
	if targetURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_URL_PAYLOAD",
			Message: "A target URL structure configuration string parameter must be specified.",
		})
	}

	var opts PrintOptions
	opts.PaperSize = c.FormValue("paperSize")
	if opts.PaperSize == "" {
		opts.PaperSize = "A4"
	}

	opts.MarginTop, _ = strconv.ParseFloat(c.FormValue("marginTop"), 64)
	opts.MarginBottom, _ = strconv.ParseFloat(c.FormValue("marginBottom"), 64)
	opts.MarginLeft, _ = strconv.ParseFloat(c.FormValue("marginLeft"), 64)
	opts.MarginRight, _ = strconv.ParseFloat(c.FormValue("marginRight"), 64)

	outputPath, err := ctrl.service.HtmlToPdf(c.UserContext(), targetURL, opts)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "WEB_EXTRACTION_FAILED",
			Message: "Web render pipeline crash layout execution error: " + err.Error(),
		})
	}
	defer func() { _ = os.Remove(outputPath) }()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("webpage_capture.pdf")
	return c.SendFile(outputPath)
}

type PrintOptions struct {
	Orientation  string  `form:"orientation" json:"orientation"`
	PaperSize    string  `form:"paperSize" json:"paperSize"`
	MarginTop    float64 `form:"marginTop" json:"marginTop"`
	MarginBottom float64 `form:"marginBottom" json:"marginBottom"`
	MarginLeft   float64 `form:"marginLeft" json:"marginLeft"`
	MarginRight  float64 `form:"marginRight" json:"marginRight"`
}

func (ctrl *Controller) ConvertMarkdownToPDF(c *fiber.Ctx) error {
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target markdown submission document.",
		})
	}

	var opts PrintOptions
	if err := c.BodyParser(&opts); err != nil {
		opts.Orientation = "portrait"
		opts.PaperSize = "A4"
	}

	outputPath, err := ctrl.service.MarkdownToPdf(c.UserContext(), upload.Path, opts)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "MARKDOWN_CONVERSION_FAILED",
			Message: "Markdown vector mapping rendering execution failure: " + err.Error(),
		})
	}
	defer func() { _ = os.Remove(outputPath) }()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_markdown_report.pdf")
	return c.SendFile(outputPath)
}

func (ctrl *Controller) ConvertCodeToPDF(c *fiber.Ctx) error {
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target script text file parameters.",
		})
	}

	var opts PrintOptions
	opts.PaperSize = c.FormValue("paperSize")
	if opts.PaperSize == "" {
		opts.PaperSize = "A4"
	}

	opts.MarginTop, _ = strconv.ParseFloat(c.FormValue("marginTop"), 64)
	opts.MarginBottom, _ = strconv.ParseFloat(c.FormValue("marginBottom"), 64)
	opts.MarginLeft, _ = strconv.ParseFloat(c.FormValue("marginLeft"), 64)
	opts.MarginRight, _ = strconv.ParseFloat(c.FormValue("marginRight"), 64)

	outputPath, err := ctrl.service.CodeToPdf(c.UserContext(), upload.Path, upload.Header.Filename, opts)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "CODE_CONVERSION_FAILED",
			Message: "Source script rendering pipeline processing crashed: " + err.Error(),
		})
	}
	defer func() { _ = os.Remove(outputPath) }()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_code_document.pdf")
	return c.SendFile(outputPath)
}

type asyncBillingLease struct {
	reservationID string
	settle        func()
	commit        func() error
}

func reserveAsyncBilling(
	c *fiber.Ctx,
	identityType string,
	identityID string,
	tool billing.Tool,
	pages int,
	images int,
	path string,
	taskID string,
) (*asyncBillingLease, error) {
	if identityType == string(identity.TypeGuest) {
		if billing.GuestQuota == nil {
			return nil, fmt.Errorf("guest quota store not configured")
		}

		reserveCtx := identity.RequestContext(c)
		reservation, err := billing.GuestQuota.Reserve(reserveCtx, identityID, tool, pages, images, path)
		if err != nil {
			return nil, err
		}

		lease := &asyncBillingLease{reservationID: reservation.ID}
		lease.settle = func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = billing.GuestQuota.Release(ctx, lease.reservationID)
		}
		lease.commit = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return billing.GuestQuota.Commit(ctx, lease.reservationID)
		}
		return lease, nil
	}

	reservation, err := billing.Default.ReserveWithTaskID(identityID, tool, pages, images, path, taskID)
	if err != nil {
		return nil, err
	}

	lease := &asyncBillingLease{reservationID: reservation.ID}
	lease.settle = func() {
		_ = billing.Default.Release(lease.reservationID)
	}
	lease.commit = func() error {
		return billing.Default.Commit(lease.reservationID)
	}
	return lease, nil
}

func (ctrl *Controller) HandleAsyncHTMLToPDF(c *fiber.Ctx) error {
	identityType, _ := c.Locals(identity.LocalIdentityType).(string)
	identityID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if identityID == "" {
		identityID = c.IP()
	}
	ownerIdentity := identityID

	targetURL := c.FormValue("url")
	if targetURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing input target webpage 'url' field resource string parameter.",
		})
	}

	taskId := uuid.New().String()

	opts := PrintOptions{}
	if paperSize := c.FormValue("paperSize"); paperSize != "" {
		opts.PaperSize = paperSize
	}
	if marginStr := c.FormValue("marginTop"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginTop = m
		}
	}
	if marginStr := c.FormValue("marginBottom"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginBottom = m
		}
	}
	if marginStr := c.FormValue("marginLeft"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginLeft = m
		}
	}
	if marginStr := c.FormValue("marginRight"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginRight = m
		}
	}

	lease, err := reserveAsyncBilling(c, identityType, identityID, billing.HTMLToPDF, 0, 0, c.Path(), taskId)
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	reservationID := lease.reservationID

	if diskErr := disk.CheckAvailableSpace(temp.GetDir(), 100*1024*1024); diskErr != nil {
		lease.settle()
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInsufficientStorage).JSON(fiber.Map{
			"code":    "INSUFFICIENT_STORAGE",
			"message": "Insufficient server disk space available to start rendering operation.",
		})
	}

	okCreated, err := tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Allocating sandboxed headless rendering nodes...", ownerIdentity, reservationID)
	if err != nil || !okCreated {
		lease.settle()
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register task"})
	}

	_ = idempotency.SetTaskID(c, taskId, nil)

	go func(id, target string, printOpts PrintOptions, reservationID, owner string) {
		taskCtx, taskCancel := context.WithCancel(context.Background())
		defer taskCancel()

		stopPoller := make(chan struct{})
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopPoller:
					return
				case <-taskCtx.Done():
					return
				case <-ticker.C:
					task, _ := tasks.Registry.Get(id)
					if task != nil && task.Status == "CANCELLED" {
						taskCancel()
						return
					}
				}
			}
		}()

		var localOutPath string
		var releaseToken func()
		billingSettled := false

		releaseBilling := func() {
			if billingSettled {
				return
			}
			billingSettled = true
			lease.settle()
		}

		defer func() {
			close(stopPoller)
			if releaseToken != nil {
				releaseToken()
			}
			if !billingSettled {
				releaseBilling()
			}
			if localOutPath != "" {
				_ = os.Remove(localOutPath)
			}
			if r := recover(); r != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", fmt.Sprintf("Headless engine pipeline fault encountered: %v", r), owner)
			}
		}()

		var acquired bool
		for attempt := 0; attempt < 30; attempt++ {
			if taskCtx.Err() != nil {
				releaseBilling()
				return
			}
			acqCtx, cancel := context.WithTimeout(taskCtx, 5*time.Second)
			rel, ok, acqErr := limiter.Default.AcquireWithRelease(acqCtx, id, owner)
			cancel()

			if acqErr != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Execution capacity service unavailable.", owner)
				return
			}
			if ok {
				acquired = true
				releaseToken = rel
				break
			}
			time.Sleep(1 * time.Second)
		}

		if !acquired {
			releaseBilling()
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Server capacity reached. Task execution timed out waiting for capacity.", owner)
			return
		}

		if taskCtx.Err() != nil {
			releaseBilling()
			return
		}

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 35, "", "Spawning layout canvas compilation layers...", owner)

		outPath, err := ctrl.service.HtmlToPdf(taskCtx, target, printOpts)
		if err != nil {
			releaseBilling()
			if taskCtx.Err() == nil {
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			}
			return
		}
		localOutPath = outPath

		if taskCtx.Err() != nil {
			releaseBilling()
			return
		}

		r2Key := fmt.Sprintf("outputs/tasks/%s/compiled.pdf", id)
		r2Store, r2Err := storage.Default()
		isProd := os.Getenv("APP_ENV") == "production"

		if r2Err != nil || r2Store == nil {
			if isProd {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Cloud storage is unconfigured in production environment.", owner)
				return
			}
			if err := lease.commit(); err != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
				return
			}
			billingSettled = true
			_, _ = tasks.Registry.SetWithKey(id, "COMPLETED", 100, outPath, "", owner)
			return
		}

		if err := r2Store.UploadFile(outPath, r2Key, "application/pdf"); err != nil {
			releaseBilling()
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Failed to save completed document to cloud storage.", owner)
			return
		}

		if err := lease.commit(); err != nil {
			_ = os.Remove(outPath)
			releaseBilling()
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed.", owner)
			return
		}

		billingSettled = true
		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			log.Printf("[CONVERSION TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, targetURL, opts, reservationID, ownerIdentity)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"taskId": taskId,
	})
}

func (ctrl *Controller) HandleAsyncMarkdownToPDF(c *fiber.Ctx) error {
	identityType, _ := c.Locals(identity.LocalIdentityType).(string)
	identityID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if identityID == "" {
		identityID = c.IP()
	}
	ownerIdentity := identityID

	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing input target Markdown file resource payload.",
		})
	}

	taskId := uuid.New().String()

	opts := PrintOptions{}
	if marginStr := c.FormValue("marginTop"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginTop = m
		}
	}
	if marginStr := c.FormValue("marginBottom"); marginStr != "" {
		if m, err := strconv.ParseFloat(marginStr, 64); err == nil {
			opts.MarginBottom = m
		}
	}

	lease, err := reserveAsyncBilling(c, identityType, identityID, billing.ConvertMarkdownToPDF, 0, 0, c.Path(), taskId)
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	reservationID := lease.reservationID

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, taskId+"-"+filepath.Base(upload.Header.Filename))
	if err := copyFile(upload.Path, inputPath); err != nil {
		lease.settle()
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Disk caching allocation error",
		})
	}

	okCreated, err := tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Initializing compilation text nodes...", ownerIdentity, reservationID)
	if err != nil || !okCreated {
		lease.settle()
		_ = os.Remove(inputPath)
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register task"})
	}

	_ = idempotency.SetTaskID(c, taskId, nil)

	go func(id, srcPath string, printOpts PrintOptions, reservationID, owner string) {
		taskCtx, taskCancel := context.WithCancel(context.Background())
		defer taskCancel()

		stopPoller := make(chan struct{})
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopPoller:
					return
				case <-taskCtx.Done():
					return
				case <-ticker.C:
					task, _ := tasks.Registry.Get(id)
					if task != nil && task.Status == "CANCELLED" {
						taskCancel()
						return
					}
				}
			}
		}()

		var localOutPath string
		var releaseToken func()
		billingSettled := false

		releaseBilling := func() {
			if billingSettled {
				return
			}
			billingSettled = true
			lease.settle()
		}

		defer func() {
			close(stopPoller)
			if releaseToken != nil {
				releaseToken()
			}
			if !billingSettled {
				releaseBilling()
			}
			_ = os.Remove(srcPath)
			if localOutPath != "" {
				_ = os.Remove(localOutPath)
			}
			if r := recover(); r != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Text compilation parser structural error.", owner)
			}
		}()

		var acquired bool
		for attempt := 0; attempt < 30; attempt++ {
			if taskCtx.Err() != nil {
				releaseBilling()
				return
			}
			acqCtx, cancel := context.WithTimeout(taskCtx, 5*time.Second)
			rel, ok, acqErr := limiter.Default.AcquireWithRelease(acqCtx, id, owner)
			cancel()

			if acqErr != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Execution capacity service unavailable.", owner)
				return
			}
			if ok {
				acquired = true
				releaseToken = rel
				break
			}
			time.Sleep(1 * time.Second)
		}

		if !acquired {
			releaseBilling()
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Server capacity reached. Task execution timed out waiting for capacity.", owner)
			return
		}

		if taskCtx.Err() != nil {
			releaseBilling()
			return
		}

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 40, "", "Parsing tokens and injecting layout styling variables...", owner)

		outPath, err := ctrl.service.MarkdownToPdf(taskCtx, srcPath, printOpts)
		if err != nil {
			releaseBilling()
			if taskCtx.Err() == nil {
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			}
			return
		}
		localOutPath = outPath

		if taskCtx.Err() != nil {
			releaseBilling()
			return
		}

		r2Key := fmt.Sprintf("outputs/tasks/%s/compiled.pdf", id)
		r2Store, r2Err := storage.Default()
		isProd := os.Getenv("APP_ENV") == "production"

		if r2Err != nil || r2Store == nil {
			if isProd {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Cloud storage is unconfigured in production environment.", owner)
				return
			}
			if err := lease.commit(); err != nil {
				releaseBilling()
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
				return
			}
			billingSettled = true
			_, _ = tasks.Registry.SetWithKey(id, "COMPLETED", 100, outPath, "", owner)
			return
		}

		if err := r2Store.UploadFile(outPath, r2Key, "application/pdf"); err != nil {
			releaseBilling()
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Failed to save completed document to cloud storage.", owner)
			return
		}

		if err := lease.commit(); err != nil {
			_ = os.Remove(outPath)
			releaseBilling()
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed.", owner)
			return
		}

		billingSettled = true
		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			log.Printf("[CONVERSION TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, inputPath, opts, reservationID, ownerIdentity)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"taskId": taskId,
	})
}

func ConvertPdfToOfficeHandler(targetFormat string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		upload, err := uploads.MustPDFFile(c, "file")
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "PDF file is required"})
		}

		if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_OFFICE_CONVERT", 200); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"code":    "PAGE_LIMIT_EXCEEDED",
				"error":   err.Error(),
				"message": err.Error(),
			})
		}

		tempDir := os.TempDir()
		_ = os.MkdirAll(tempDir, os.ModePerm)

		fileID := uuid.New().String()
		outputFilePath := filepath.Join(tempDir, fmt.Sprintf("%s.%s", fileID, targetFormat))
		defer func() { _ = os.Remove(outputFilePath) }()

		err = ProcessOfficeConversion(targetFormat, upload.Path, outputFilePath)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Conversion failed: " + err.Error()})
		}

		c.Set("Content-Type", "application/octet-stream")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.%s", upload.Header.Filename, targetFormat))

		return c.SendFile(outputFilePath)
	}
}

func (ctrl *Controller) ConvertCustomImagesToPDF(c *fiber.Ctx) error {
	layoutData := c.FormValue("canvasLayout")
	if layoutData == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_LAYOUT_METADATA",
			Message: "Custom canvas configuration mapping data is required.",
		})
	}

	var layout []CanvasLayoutItem
	if err := json.Unmarshal([]byte(layoutData), &layout); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MALFORMED_LAYOUT_JSON",
			Message: "Failed to parse structural canvas coordinate parameters accurately.",
		})
	}

	files, err := uploads.MustFiles(c, "images")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one image binary is required for layout mapping context.",
		})
	}

	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one image binary is required for layout mapping context.",
		})
	}

	temporaryImagePaths := make([]string, 0, len(files))
	for _, file := range files {
		temporaryImagePaths = append(temporaryImagePaths, file.Path)
	}

	outputPath, err := ctrl.service.CustomImagesToPDF(temporaryImagePaths, layout)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "CUSTOM_PDF_COMPILATION_FAILED",
			Message: "Error rendering layered graphic objects to target PDF matrix: " + err.Error(),
		})
	}
	defer func() { _ = os.Remove(outputPath) }()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("custom_compiled_images.pdf")
	return c.SendFile(outputPath)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}
