package security

import (
	"log"
	"os"
	"path/filepath"
	"strings"

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

func (ctrl *Controller) Lock(c *fiber.Ctx) error {
	password := c.FormValue("password")
	if password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_PASSWORD",
			Message: "Password field is required to encrypt this file.",
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Invalid or missing file upload parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save security input target path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate workspace scratch environment parameters.",
		})
	}

	defer func() {
		if removeErr := os.Remove(inputPath); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary input PDF at %s: %v", inputPath, removeErr)
		}
	}()

	outputPath, err := ctrl.service.EncryptPDF(inputPath, password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "ENCRYPTION_ENGINE_FAILED",
			Message: "Encryption pipeline failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("locked_" + filepath.Base(fileHeader.Filename))

	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to delete temporary encrypted PDF at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) Unlock(c *fiber.Ctx) error {
	password := c.FormValue("password")
	if password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_PASSWORD",
			Message: "Password is required to unlock this file.",
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Failed to save security unlock target path %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to initialize scratch space structures.",
		})
	}

	defer func() {
		if removeErr := os.Remove(inputPath); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary locked input PDF at %s: %v", inputPath, removeErr)
		}
	}()

	outputPath, err := ctrl.service.DecryptPDF(inputPath, password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(APIError{
			Code:    "DECRYPTION_AUTH_FAILED",
			Message: "Invalid security password or corrupted document structure.",
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("unlocked_" + filepath.Base(fileHeader.Filename))

	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Failed to delete temporary decrypted PDF at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (h *Controller) HandleRedaction(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing target file stream"})
	}

	keywordsStr := c.FormValue("keywords")
	// Allow empty keywords if custom drawing zones are provided instead
	var keywords []string
	if keywordsStr != "" {
		rawKeywords := strings.Split(keywordsStr, ",")
		for _, k := range rawKeywords {
			trimmed := strings.TrimSpace(k)
			if trimmed != "" {
				keywords = append(keywords, trimmed)
			}
		}
	}

	// Capture the JSON coordinates string array passed from the frontend canvas handler
	boxesStr := c.FormValue("boxes")
	if keywordsStr == "" && (boxesStr == "" || boxesStr == "[]") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Provide either text keywords or drag manual redact areas."})
	}

	// Save temp staging asset file to system scratch directory
	tempInPath := filepath.Join(os.TempDir(), fileHeader.Filename)
	if err := c.SaveFile(fileHeader, tempInPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Workspace directory file locked"})
	}
	defer os.Remove(tempInPath)

	// Execute multi-page global service logic pipeline with drawing support bounds
	outFileName, err := h.service.RedactPageText(tempInPath, os.TempDir(), keywords, boxesStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	fullOutPath := filepath.Join(os.TempDir(), outFileName)
	defer os.Remove(fullOutPath)

	return c.Download(fullOutPath)
}
