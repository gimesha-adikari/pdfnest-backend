package ocr

import (
	"log"
	"os"
	"path/filepath"

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
			log.Printf("[CLEANUP WARNING] Failed to delete temporary uploaded OCR input PDF at %s: %v", inputPath, err)
		}
	}()

	txtOutputPath, err := ctrl.service.ExtractTextFromPDF(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OCR_EXTRACTION_FAILED",
			Message: "OCR processing engine pipeline failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "text/plain")
	c.Attachment("extracted_text.txt")
	err = c.SendFile(txtOutputPath)

	if cleanupErr := os.Remove(txtOutputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to purge intermediate text asset at %s: %v", txtOutputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) ConvertImagesToTextPDF(c *fiber.Ctx) error {
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_MULTIPART_FORM",
			Message: "Invalid multipart form asset data matrix transmission.",
		})
	}

	files := form.File["images"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "At least one graphic asset is required for text processing compilation.",
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

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to purge temporary output text PDF at %s: %v", outputPath, cleanupErr)
	}

	return err
}
