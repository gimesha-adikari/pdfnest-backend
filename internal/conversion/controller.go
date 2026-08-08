package conversion

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/uploads"
	"strconv"

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

	imageType := c.FormValue("image_type", "jpg")

	zipOutputPath, err := ctrl.service.PdfToImagesBackend(upload.Path, imageType)
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

	imgBytes, err := cc.service.ConvertPageToImageStream(upload.Header, pageNum, scale)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{
			"code":    "RASTER_ENGINE_CRASH",
			"message": err.Error(),
		})
	}

	c.Set("Content-Type", "image/jpeg")
	c.Set("Content-Length", strconv.Itoa(length(imgBytes)))
	c.Set("Cache-Control", "public, max-age=60")

	return c.Send(imgBytes)
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

	outputPath, err := ctrl.service.OfficeToPdf(upload.Path)
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

	outputPath, err := ctrl.service.HtmlToPdf(targetURL, opts)
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

	outputPath, err := ctrl.service.MarkdownToPdf(upload.Path, opts)
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

	outputPath, err := ctrl.service.CodeToPdf(upload.Path, upload.Header.Filename, opts)
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

func (ctrl *Controller) HandleAsyncHTMLToPDF(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	targetURL := c.FormValue("url")
	if targetURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing input target webpage 'url' field resource string parameter.",
		})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Allocating sandboxed headless rendering nodes...", "")

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

	release, ok := limiter.Default.TryAcquire()
	if !ok {
		c.Set("Retry-After", "5")
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"code":    "SERVER_BUSY",
			"error":   "Server processing capacity reached. Please try again in a few seconds.",
			"message": "Server processing capacity reached. Please try again in a few seconds.",
		})
	}

	reservation, err := billing.Default.Reserve(userID, billing.HTMLToPDF, 0, 0, c.Path())
	if err != nil {
		release()
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	go func(id, target string, printOpts PrintOptions, reservationID string, releaseToken func()) {
		defer func() {
			releaseToken()
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				tasks.Registry.Set(id, "FAILED", 0, "", fmt.Sprintf("Headless engine pipeline fault encountered: %v", r))
			}
		}()

		tasks.Registry.Set(id, "PROCESSING", 35, "Spawning layout canvas compilation layers...", "")

		outPath, err := ctrl.service.HtmlToPdf(target, printOpts)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", "Billing finalization failed")
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")
	}(taskId, targetURL, opts, reservation.ID, release)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) HandleAsyncMarkdownToPDF(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing input target Markdown file resource payload.",
		})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Initializing compilation text nodes...", "")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, taskId+"-"+filepath.Base(upload.Header.Filename))
	if err := copyFile(upload.Path, inputPath); err != nil {
		tasks.Registry.Set(taskId, "FAILED", 0, "", "Workspace scratch write failure occurred.")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Disk caching allocation error",
		})
	}

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

	release, ok := limiter.Default.TryAcquire()
	if !ok {
		_ = os.Remove(inputPath)
		c.Set("Retry-After", "5")
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"code":    "SERVER_BUSY",
			"error":   "Server processing capacity reached. Please try again in a few seconds.",
			"message": "Server processing capacity reached. Please try again in a few seconds.",
		})
	}

	reservation, err := billing.Default.Reserve(
		userID,
		billing.ConvertMarkdownToPDF,
		0,
		0,
		c.Path(),
	)
	if err != nil {
		release()
		_ = os.Remove(inputPath)

		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	go func(id, srcPath string, printOpts PrintOptions, reservationID string, releaseToken func()) {
		defer func() {
			releaseToken()
			_ = os.Remove(srcPath)

			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)

				tasks.Registry.Set(
					id,
					"FAILED",
					0,
					"",
					"Text compilation parser structural error.",
				)
			}
		}()

		tasks.Registry.Set(
			id,
			"PROCESSING",
			40,
			"Parsing tokens and injecting layout styling variables...",
			"",
		)

		outPath, err := ctrl.service.MarkdownToPdf(srcPath, printOpts)
		if err != nil {
			_ = billing.Default.Release(reservationID)

			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = os.Remove(outPath)
			_ = billing.Default.Release(reservationID)

			tasks.Registry.Set(
				id,
				"FAILED",
				0,
				"",
				"Billing finalization failed.",
			)
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")

	}(taskId, inputPath, opts, reservation.ID, release)

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
