package ocr

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"

	"pdfnest-backend/internal/tasks"

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

func (ctrl *Controller) ProcessOCR(c *fiber.Ctx) error {

	userID := c.Locals("user_id").(string)

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
		log.Printf("[SERVER ERROR] Failed to save source PDF for OCR processing at %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate workspace scratch environment parameters.",
		})
	}

	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary uploaded OCR source file: %v", err)
		}
	}()

	outputPath, err := ctrl.service.ExtractTextFromPDF(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OCR_PROCESSING_FAILED",
			Message: "Tesseract engine pipeline failed extraction process: " + err.Error(),
		})
	}

	defer func() {
		if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary output data file: %v", err)
		}
	}()

	c.Set("Content-Type", "text/plain")
	c.Attachment(filepath.Base(outputPath))

	config.LogToolUsage(userID, "extract_text_from_pdf")

	return c.SendFile(outputPath)
}

func (ctrl *Controller) ProcessImageToTextPDF(c *fiber.Ctx) error {

	userID := c.Locals("user_id").(string)

	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_MULTIPART_FORM",
			Message: "Multipart payload format is corrupted or parsing structure is broken.",
		})
	}

	files := form.File["images"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_IMAGE_DATASET",
			Message: "No valid file matrices array detected within the 'images' field required for text processing compilation.",
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
			log.Printf("[SERVER ERROR] Failed to allocate file to workspace path %s: %v", uniquePath, err)
			return c.Status(fiber.StatusInternalServerError).JSON(APIError{
				Code:    "DISK_WRITE_FAILURE",
				Message: "Failed to allocate workspace processing paths.",
			})
		}
		temporaryImagePaths = append(temporaryImagePaths, uniquePath)
	}

	outputPath, err := ctrl.service.ImageToTextPDF(temporaryImagePaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "SEARCHABLE_PDF_COMPILATION_FAILED",
			Message: "Smart image extraction pipeline failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("ocr_processed_document.pdf")
	err = c.SendFile(outputPath)

	_ = os.Remove(outputPath)

	if err == nil {
		config.LogToolUsage(userID, "image_to_text_pdf")
	}

	return err
}

func (ctrl *Controller) HandleAsyncExtractText(c *fiber.Ctx) error {

	userID := c.Locals("user_id").(string)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "MISSING_FILE", Message: "No file uploaded"})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Initializing Document Ingestion Matrix...", "")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, taskId+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(500).JSON(APIError{Code: "DISK_ERR", Message: "Failed to write workspace data cache"})
	}

	go func(id, srcPath string) {
		defer func() {
			_ = os.Remove(srcPath)
			if r := recover(); r != nil {
				tasks.Registry.Set(id, "FAILED", 0, "", "Subprocess thread failure occurred.")
			}
		}()

		tasks.Registry.Set(id, "PROCESSING", 25, "Running Ghostscript page raster engines...", "")
		outPath, err := ctrl.service.ExtractTextFromPDF(srcPath)
		if err != nil {
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")

		config.LogToolUsage(userID, "extract_text_from_pdf")
	}(taskId, inputPath)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) HandleAsyncImageToTextPDF(c *fiber.Ctx) error {

	userID := c.Locals("user_id").(string)

	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_FORM", Message: "Form structure processing error"})
	}

	files := form.File["images"]
	if len(files) == 0 {
		return c.Status(400).JSON(APIError{Code: "MISSING_IMAGES", Message: "No file targets dropped inside body array"})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Allocating compilation environment nodes...", "")

	tempDir := os.TempDir()
	var tempPaths []string
	for _, f := range files {
		path := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(f.Filename))
		if err := c.SaveFile(f, path); err == nil {
			tempPaths = append(tempPaths, path)
		}
	}

	go func(id string, imgPaths []string) {
		defer func() {
			for _, p := range imgPaths {
				_ = os.Remove(p)
			}
			if r := recover(); r != nil {
				tasks.Registry.Set(id, "FAILED", 0, "", "Subprocess matrix generation fault.")
			}
		}()

		tasks.Registry.Set(id, "PROCESSING", 35, "Scanning character grid topologies and building PDF layout layers...", "")
		outPath, err := ctrl.service.ImageToTextPDF(imgPaths)
		if err != nil {
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")

		config.LogToolUsage(userID, "image_to_text_pdf")

	}(taskId, tempPaths)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}
