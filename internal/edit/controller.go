package edit

import (
	"encoding/json"
	"io"
	"net/http"
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

func (cr *Controller) HandleExtractHTML(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error":   "PDF file parameter is required",
		})
	}

	filePassword := c.FormValue("file_password")

	tempPdfPath := filepath.Join(os.TempDir(), "source_"+uuid.New().String()+".pdf")
	if err := c.SaveFile(fileHeader, tempPdfPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error":   "Failed to stage original document on server disk",
		})
	}

	submission, err := cr.service.ExtractLayout(tempPdfPath, filePassword)
	if err != nil {
		_ = os.Remove(tempPdfPath)
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
		"source_tracker": tempPdfPath,
	})
}

func (cr *Controller) HandleCompilePDF(c *fiber.Ctx) error {
	payloadBytes := c.Body()

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

	submission, err := cr.service.CompileLayout(tracker.SourceTracker, payloadBytes)
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
