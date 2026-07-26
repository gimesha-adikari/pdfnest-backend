package markup

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"pdfnest-backend/internal/uploads"
	"strings"

	"pdfnest-backend/internal/storage"

	"github.com/gofiber/fiber/v2"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

func (cr *Controller) HandleHighlight(c *fiber.Ctx) error {
	return cr.handle(c, ActionHighlight)
}

func (cr *Controller) HandleUnderline(c *fiber.Ctx) error {
	return cr.handle(c, ActionUnderline)
}

func (cr *Controller) HandleStrikeout(c *fiber.Ctx) error {
	return cr.handle(c, ActionStrikeout)
}

func (cr *Controller) handle(c *fiber.Ctx, action Action) error {
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "PDF file parameter is required",
		})
	}

	boxesStr := c.FormValue("boxes")
	filePassword := c.FormValue("file_password")

	mode := strings.ToLower(strings.TrimSpace(c.FormValue("mode")))
	if mode == "" {
		mode = "smart"
	}

	var boxes []Box
	if err := json.Unmarshal([]byte(boxesStr), &boxes); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "Invalid boxes JSON payload",
		})
	}

	store, err := storage.Default()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	contentType := upload.Header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/pdf"
	}

	sourceKey := storage.BuildKey(
		"markup/source",
		filepath.Ext(upload.Header.Filename),
	)

	if err := store.UploadFile(upload.Path, sourceKey, contentType); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   fmt.Sprintf("Failed to upload original PDF to R2: %v", err),
		})
	}

	payloadBytes, err := json.Marshal(map[string]any{
		"boxes":         boxes,
		"mode":          mode,
		"file_password": filePassword,
		"action":        action,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   fmt.Sprintf("Failed to encode markup payload: %v", err),
		})
	}

	payloadKey := storage.BuildKey("markup/payload", ".json")
	if err := store.UploadBytes(payloadBytes, payloadKey, "application/json"); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   fmt.Sprintf("Failed to upload markup payload to R2: %v", err),
		})
	}

	var submission *workerJobSubmission

	switch action {
	case ActionHighlight:
		submission, err = cr.service.HighlightPDF(
			sourceKey,
			payloadKey,
			upload.Header.Filename,
		)

	case ActionUnderline:
		submission, err = cr.service.UnderlinePDF(
			sourceKey,
			payloadKey,
			upload.Header.Filename,
		)

	case ActionStrikeout:
		submission, err = cr.service.StrikeoutPDF(
			sourceKey,
			payloadKey,
			upload.Header.Filename,
		)

	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "Unsupported action",
		})
	}

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	return c.Status(fiber.StatusAccepted).JSON(JobSubmissionResponse{
		Success:   true,
		JobID:     submission.JobID,
		Status:    submission.Status,
		QueueName: submission.QueueName,
	})
}

func (cr *Controller) HandleJobStatus(c *fiber.Ctx) error {
	jobID := c.Params("job_id")

	job, err := cr.service.GetJobStatus(jobID)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	return c.JSON(job)
}

func (cr *Controller) HandleJobDownload(c *fiber.Ctx) error {
	jobID := c.Params("job_id")

	resp, err := cr.service.GetJobDownload(jobID)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}
	defer resp.Body.Close()

	pdfBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Set("Content-Type", ct)
	} else {
		c.Set("Content-Type", "application/pdf")
	}

	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		c.Set("Content-Disposition", cd)
	}

	return c.Send(pdfBytes)
}
