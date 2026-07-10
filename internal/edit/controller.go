// file: internal/edit/controller.go
package edit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/helper"

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

	userID := c.Locals("user_id").(string)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "PDF file parameter is required",
		})
	}

	tempPdfPath := filepath.Join(os.TempDir(), "source_"+uuid.New().String()+".pdf")
	if err := c.SaveFile(fileHeader, tempPdfPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to stage original document on server disk",
		})
	}

	layoutBytes, err := cr.service.ExtractLayout(tempPdfPath)
	if err != nil {
		os.Remove(tempPdfPath)

		println("==================== PYTHON RUNTIME ERROR CRASH DUMP ====================")
		println(err.Error())
		println("=========================================================================")

		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "An unexpected error occurred while processing the document. Please try again."})
	}

	var mappedResponse map[string]interface{}
	if err := json.Unmarshal(layoutBytes, &mappedResponse); err != nil {
		os.Remove(tempPdfPath)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to decode coordinate map sequence from layout analyzer",
		})
	}

	mappedResponse["source_tracker"] = tempPdfPath

	config.LogToolUsage(userID, "pdf_edit_extract", helper.CheckCreditUsage(c))

	return c.JSON(mappedResponse)
}

func (cr *Controller) HandleCompilePDF(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	payloadBytes := c.Body()

	println(string(payloadBytes))

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

	if _, err := os.Stat(tracker.SourceTracker); os.IsNotExist(err) {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"success": false,
			"error":   "The original file staging window expired. Please re-upload the document",
		})
	}
	fullOutPath, err := cr.service.CompileLayout(tracker.SourceTracker, payloadBytes)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	outPdfName := "edited_" + filepath.Base(fullOutPath)
	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", "attachment; filename="+outPdfName)

	err = c.SendFile(fullOutPath)

	if cleanupErr := os.Remove(fullOutPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		println("[CLEANUP WARNING] Failed to purge temporary output compiled PDF:", cleanupErr.Error())
	}

	if err == nil {
		config.LogToolUsage(userID, "pdf_edit_compile", helper.CheckCreditUsage(c))
	}

	return err
}
