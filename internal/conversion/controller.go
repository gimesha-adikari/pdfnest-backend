package conversion

import (
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) ConvertImagesToPDF(c *fiber.Ctx) error {
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_MULTIPART_FORM",
			Message: "Invalid multipart form transmission asset array.",
		})
	}

	files := form.File["images"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one image file is required for compilation.",
		})
	}

	tempDir := os.TempDir()
	var temporaryImagePaths []string

	defer func() {
		for _, path := range temporaryImagePaths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("[CLEANUP WARNING] Failed to delete temporary input image at %s: %v", path, err)
			}
		}
	}()

	for _, fileHeader := range files {
		uniquePath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
		if err := c.SaveFile(fileHeader, uniquePath); err != nil {
			log.Printf("[SERVER ERROR] Failed to save multipart file to unique path %s: %v", uniquePath, err)
			return c.Status(fiber.StatusInternalServerError).JSON(APIError{
				Code:    "DISK_WRITE_FAILURE",
				Message: "Failed to initialize internal workspace allocation streams.",
			})
		}
		temporaryImagePaths = append(temporaryImagePaths, uniquePath)
	}

	outputPath, err := ctrl.service.ImagesToPDF(temporaryImagePaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "PDF_COMPILATION_FAILED",
			Message: "Image matrix processing pipeline failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_images.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to purge temporary output compiled PDF at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) RasterizePdfUniversal(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF file upload parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save source PDF target upload path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate local scratch environment parameters.",
		})
	}

	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary uploaded input PDF at %s: %v", inputPath, err)
		}
	}()

	zipOutputPath, err := ctrl.service.PdfToImagesBackend(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "RASTERIZATION_FAILED",
			Message: "PDF extraction routine runtime failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/zip")
	c.Attachment("extracted_pages.zip")
	err = c.SendFile(zipOutputPath)

	if cleanupErr := os.Remove(zipOutputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to delete temporary output ZIP file archive at %s: %v", zipOutputPath, cleanupErr)
	}

	return err
}

func (cc *Controller) StreamPagePreviewHandler(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
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

	imgBytes, err := cc.service.ConvertPageToImageStream(file, pageNum, scale)
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
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source document upload asset parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{Code: "DISK_WRITE_FAILURE", Message: err.Error()})
	}
	defer func() { _ = os.Remove(inputPath) }()

	outputPath, err := ctrl.service.OfficeToPdf(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OFFICE_CONVERSION_FAILED",
			Message: "Office calculation matrix routine failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("converted_office_doc.pdf")
	err = c.SendFile(outputPath)

	defer func() { _ = os.Remove(outputPath) }()
	return err
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

	c.Set("Content-Type", "application/pdf")
	c.Attachment("webpage_capture.pdf")
	err = c.SendFile(outputPath)

	defer func() { _ = os.Remove(outputPath) }()
	return err
}

type PrintOptions struct {
	Orientation  string  `form:"orientation" json:"orientation"` // "portrait" or "landscape"
	PaperSize    string  `form:"paperSize" json:"paperSize"`     // "A4", "letter", "legal"
	MarginTop    float64 `form:"marginTop" json:"marginTop"`
	MarginBottom float64 `form:"marginBottom" json:"marginBottom"`
	MarginLeft   float64 `form:"marginLeft" json:"marginLeft"`
	MarginRight  float64 `form:"marginRight" json:"marginRight"`
}

func (ctrl *Controller) ConvertMarkdownToPDF(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
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

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{Code: "DISK_WRITE_FAILURE", Message: err.Error()})
	}
	defer func() { _ = os.Remove(inputPath) }()

	outputPath, err := ctrl.service.MarkdownToPdf(inputPath, opts)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "MARKDOWN_CONVERSION_FAILED",
			Message: "Markdown vector mapping rendering execution failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_markdown_report.pdf")
	err = c.SendFile(outputPath)

	defer func() { _ = os.Remove(outputPath) }()
	return err
}

func (ctrl *Controller) ConvertCodeToPDF(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
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

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{Code: "DISK_WRITE_FAILURE", Message: err.Error()})
	}
	defer func() { _ = os.Remove(inputPath) }()

	outputPath, err := ctrl.service.CodeToPdf(inputPath, fileHeader.Filename, opts)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "CODE_CONVERSION_FAILED",
			Message: "Source script rendering pipeline processing crashed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("compiled_code_document.pdf")
	err = c.SendFile(outputPath)

	defer func() { _ = os.Remove(outputPath) }()
	return err
}
