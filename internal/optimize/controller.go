package optimize

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"

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

	userID := c.Locals("user_id").(string)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document file parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save target compression PDF to path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate local scratch workspace metrics.",
		})
	}

	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary unoptimized input PDF at %s: %v", inputPath, err)
		}
	}()

	outputPath, err := ctrl.service.OptimizePDF(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "COMPRESSION_ENGINE_FAILED",
			Message: "Compression processing failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("optimized_" + filepath.Base(fileHeader.Filename))

	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to delete temporary optimized output PDF at %s: %v", outputPath, cleanupErr)
	}

	if err == nil {
		config.LogToolUsage(userID, "compress")
	}

	return err
}

func (ctrl *Controller) Grayscale(c *fiber.Ctx) error {

	userID := c.Locals("user_id").(string)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document file parameter.",
		})
	}

	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	inputPath := filepath.Join(tempDir, sessionID+"-input-"+filepath.Base(fileHeader.Filename))
	outputPath := filepath.Join(tempDir, sessionID+"-output-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate local scratch workspace.",
		})
	}

	defer func() {
		os.Remove(inputPath)
		os.Remove(outputPath)
	}()

	if err := ConvertToGrayscale(inputPath, outputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "GRAYSCALE_ENGINE_FAILED",
			Message: "Color conversion failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("grayscale_" + filepath.Base(fileHeader.Filename))

	config.LogToolUsage(userID, "grayscale")

	return c.SendFile(outputPath)
}
