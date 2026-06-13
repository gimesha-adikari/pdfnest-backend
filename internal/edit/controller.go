// file: internal/edit/controller.go
package edit

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{
		service: s,
	}
}

func (cr *Controller) HandleExtractHTML(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "PDF file parameter is required",
		})
	}

	// Stage the original uploaded file to system temp workspace
	tempPdfPath := filepath.Join(os.TempDir(), "source_"+uuid.New().String()+".pdf")
	if err := c.SaveFile(fileHeader, tempPdfPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to stage original document on server disk",
		})
	}

	// Invoke the precision coordinate extraction service pipeline
	layoutBytes, err := cr.service.ExtractLayout(tempPdfPath)
	if err != nil {
		os.Remove(tempPdfPath)

		// CRITICAL ADDITION: Print the hidden python error dump directly onto your console logs
		println("==================== PYTHON RUNTIME ERROR CRASH DUMP ====================")
		println(err.Error())
		println("=========================================================================")

		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	// Parse JSON bytes array returned from our python extractor script
	var mappedResponse map[string]interface{}
	if err := json.Unmarshal(layoutBytes, &mappedResponse); err != nil {
		os.Remove(tempPdfPath)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to decode coordinate map sequence from layout analyzer",
		})
	}

	// Inject the temporary absolute file path tracking variables safely
	mappedResponse["source_tracker"] = tempPdfPath

	return c.JSON(mappedResponse)
}

func (cr *Controller) HandleCompilePDF(c *fiber.Ctx) error {
	payloadBytes := c.Body()

	// Unmarshal request wrapper properties to verify tracking details
	var tracker struct {
		SourceTracker string `json:"source_tracker"`
	}
	if err := json.Unmarshal(payloadBytes, &tracker); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "Invalid input formatting layout payload received",
		})
	}

	if tracker.SourceTracker == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "Original tracking token sequence is missing or empty",
		})
	}

	// Ensure the original source document hasn't been cleaned up or deleted yet
	if _, err := os.Stat(tracker.SourceTracker); os.IsNotExist(err) {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"success": false,
			"error":   "The original file staging window expired. Please re-upload the document",
		})
	}
	defer os.Remove(tracker.SourceTracker)

	// Execute high-fidelity overlay string injection script
	fullOutPath, err := cr.service.CompileLayout(tracker.SourceTracker, payloadBytes)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}
	defer os.Remove(fullOutPath)

	return c.Download(fullOutPath)
}
