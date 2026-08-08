package optimize

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/uploads"

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

func (ctrl *Controller) Compress(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document file parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_COMPRESS", 500); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	log.Printf("Filename: %s", upload.Header.Filename)
	log.Printf("Size: %d", upload.Header.Size)

	outputPath, err := ctrl.service.OptimizePDF(upload.Path)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "COMPRESSION_ENGINE_FAILED",
			Message: "Compression processing failure: " + err.Error(),
		})
	}
	defer func() {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary optimized output PDF at %s: %v", outputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("optimized_" + filepath.Base(upload.Header.Filename))

	return c.SendFile(outputPath)
}

func (ctrl *Controller) Grayscale(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document file parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GRAYSCALE", 500); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	sessionID := uuid.New().String()
	outputPath := filepath.Join(os.TempDir(), sessionID+"-output-"+filepath.Base(upload.Header.Filename))

	if err := ConvertToGrayscale(upload.Path, outputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "GRAYSCALE_ENGINE_FAILED",
			Message: "Color conversion failure: " + err.Error(),
		})
	}
	defer func() {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary grayscale output PDF at %s: %v", outputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("grayscale_" + filepath.Base(upload.Header.Filename))

	return c.SendFile(outputPath)
}
