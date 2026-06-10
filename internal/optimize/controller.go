package optimize

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

func (ctrl *Controller) Compress(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing target PDF document file")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save target compression PDF to path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to initialize workspace file")
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary unoptimized input PDF at %s: %v", name, err)
		}
	}(inputPath)

	outputPath, err := ctrl.service.OptimizePDF(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Compression processing failure: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary optimized output PDF at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")

	return c.Download(outputPath)
}
