package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

func (cr *Controller) HandleExtractHTML(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "PDF file parameter is required",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "PAGE_LIMIT_EXCEEDED",
			"success": false,
			"error":   err.Error(),
		})
	}

	filePassword := c.FormValue("file_password")

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

	sourceKey := storage.BuildKey("edit/source", filepath.Ext(upload.Header.Filename))
	if err := store.UploadFile(upload.Path, sourceKey, contentType); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   fmt.Sprintf("Failed to upload original PDF to R2: %v", err),
		})
	}

	submission, err := cr.service.ExtractLayout(sourceKey, filePassword, upload.Header.Filename)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"success":        true,
		"job_id":         submission.JobID,
		"status":         submission.Status,
		"queue_name":     submission.QueueName,
		"source_tracker": sourceKey,
		"source_name":    upload.Header.Filename,
	})
}

func (cr *Controller) HandleCompilePDF(c *fiber.Ctx) error {
	payloadBytes := c.Body()

	var tracker struct {
		SourceTracker string `json:"source_tracker"`
		SourceName    string `json:"source_name,omitempty"`
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

	store, err := storage.Default()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	pagesJSONKey := storage.BuildKey("edit/layout", ".json")
	if err := store.UploadBytes(payloadBytes, pagesJSONKey, "application/json"); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   fmt.Sprintf("Failed to upload edit payload to R2: %v", err),
		})
	}

	sourceName := tracker.SourceName
	if sourceName == "" {
		sourceName = filepath.Base(tracker.SourceTracker)
	}

	submission, err := cr.service.CompileLayout(tracker.SourceTracker, pagesJSONKey, sourceName)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"success":    true,
		"job_id":     submission.JobID,
		"status":     submission.Status,
		"queue_name": submission.QueueName,
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

	job, err := cr.service.GetJobStatus(jobID)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to fetch job status: " + err.Error(),
		})
	}

	resp, err := cr.service.GetJobDownload(jobID)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"success": false,
			"error":   err.Error(),
		})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return c.Status(resp.StatusCode).Send(b)
	}

	pdfBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	go func(record *WorkerJobRecord) {
		store, err := storage.Default()
		if err != nil || record == nil {
			return
		}

		ctx := context.Background()

		if record.Result != nil {
			if artifact, ok := record.Result["artifact_key"].(string); ok && artifact != "" {
				_ = store.DeleteObject(ctx, artifact)
			}
		}

		if record.Payload != nil {
			if src, ok := record.Payload["source_key"].(string); ok && src != "" {
				_ = store.DeleteObject(ctx, src)
			}
			if pages, ok := record.Payload["pages_json_key"].(string); ok && pages != "" {
				_ = store.DeleteObject(ctx, pages)
			}
		}
	}(job)

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

func (cr *Controller) HandleGetFile(c *fiber.Ctx) error {
	path := c.Query("path")
	if path == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "path query parameter is required"})
	}

	store, err := storage.Default()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "storage not configured"})
	}

	tmpPath, err := store.DownloadToTemp(path, "preview", ".pdf")
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "file not found or decryption failed"})
	}
	defer os.Remove(tmpPath)

	pdfBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read decrypted file"})
	}

	c.Set("Content-Type", "application/pdf")
	return c.Send(pdfBytes)
}
