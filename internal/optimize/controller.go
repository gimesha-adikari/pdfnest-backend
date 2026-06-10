package optimize

import (
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
	inputPath := filepath.Join(tempDir, uuid.New().String()+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to initialize workspace file")
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			_ = err
		}
	}(inputPath)

	outputPath, err := ctrl.service.OptimizePDF(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Compression processing failure")
	}

	c.Set("Content-Type", "application/pdf")
	err = c.Download(outputPath)

	err = os.Remove(outputPath)
	if err != nil {
		return err
	}
	return err
}
