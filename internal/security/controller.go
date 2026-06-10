package security

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

func (ctrl *Controller) Lock(c *fiber.Ctx) error {
	password := c.FormValue("password")
	if password == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Password field is required")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid or missing file upload")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save security input target path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to process workspace file")
	}

	defer func(name string) {
		if removeErr := os.Remove(name); removeErr != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary input PDF at %s: %v", name, removeErr)
		}
	}(inputPath)

	outputPath, err := ctrl.service.EncryptPDF(inputPath, password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Error executing encryption engine: " + err.Error())
	}

	defer func(name string) {
		if removeErr := os.Remove(name); removeErr != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary encrypted output PDF at %s: %v", name, removeErr)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) Unlock(c *fiber.Ctx) error {
	password := c.FormValue("password")
	if password == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Password is required to unlock this file")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing target PDF document")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save security unlock target path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to initialize workspace file")
	}

	defer func(name string) {
		if removeErr := os.Remove(name); removeErr != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary locked input PDF at %s: %v", name, removeErr)
		}
	}(inputPath)

	outputPath, err := ctrl.service.DecryptPDF(inputPath, password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString("Invalid security password or corrupted document structure")
	}

	defer func(name string) {
		if removeErr := os.Remove(name); removeErr != nil {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary decrypted output PDF at %s: %v", name, removeErr)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}
