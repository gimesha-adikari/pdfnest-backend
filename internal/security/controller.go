package security

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
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

	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Invalid or missing file upload parameter.",
		})
	}

	outputPath, err := ctrl.service.EncryptPDF(upload.Path, password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "ENCRYPTION_ENGINE_FAILED",
			Message: "Encryption pipeline failure: " + err.Error(),
		})
	}
	defer func() {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary encrypted PDF at %s: %v", outputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("locked_" + filepath.Base(upload.Header.Filename))

	return c.SendFile(outputPath)
}

func (ctrl *Controller) Unlock(c *fiber.Ctx) error {
	password := c.FormValue("password")
	if password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_PASSWORD",
			Message: "Password is required to unlock this file.",
		})
	}

	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document parameter.",
		})
	}

	outputPath, err := ctrl.service.DecryptPDF(upload.Path, password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(APIError{
			Code:    "DECRYPTION_AUTH_FAILED",
			Message: "Invalid security password or corrupted document structure.",
		})
	}
	defer func() {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary decrypted PDF at %s: %v", outputPath, cleanupErr)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("unlocked_" + filepath.Base(upload.Header.Filename))

	return c.SendFile(outputPath)
}

func (h *Controller) HandleRedaction(c *fiber.Ctx) error {
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing target file stream"})
	}

	keywordsStr := c.FormValue("keywords")
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

	boxesStr := c.FormValue("boxes")
	if keywordsStr == "" && (boxesStr == "" || boxesStr == "[]") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Provide either text keywords or drag manual redact areas."})
	}

	outFileName, err := h.service.RedactPageText(upload.Path, os.TempDir(), keywords, boxesStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	fullOutPath := filepath.Join(os.TempDir(), outFileName)
	defer func() {
		if cleanupErr := os.Remove(fullOutPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary redaction output at %s: %v", fullOutPath, cleanupErr)
		}
	}()

	return c.Download(fullOutPath)
}
