package ocr

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type OCRR2JobRequest struct {
	Tool      string       `json:"tool"`
	Lang      string       `json:"lang"`
	SessionID string       `json:"sessionId"`
	Files     []R2ImageRef `json:"files"`
}

func (ctrl *Controller) HandleAsyncImageToTextPDFR2(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string)
	if !ok || strings.TrimSpace(userID) == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(APIError{
			Code:    "UNAUTHORIZED",
			Message: "Missing authenticated user context.",
		})
	}

	var req OCRR2JobRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_JSON",
			Message: "Invalid OCR job payload: " + err.Error(),
		})
	}

	if len(req.Files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "No image references provided.",
		})
	}

	for _, ref := range req.Files {
		if !storage.IsOwnedKey(ref.Key, userID, "image_to_text_pdf") {
			return c.Status(fiber.StatusForbidden).JSON(APIError{Code: "FORBIDDEN_STORAGE_REFERENCE", Message: "Upload the images again before submitting this job."})
		}
	}
	lang := strings.TrimSpace(req.Lang)
	if lang == "" {
		lang = "eng"
	}

	taskId := uuid.New().String()

	// Billing is based on submitted images, not images that happen to contain text.
	reservation, err := billing.Default.ReserveWithTaskID(userID, billing.ImageToTextPDF, 0, len(req.Files), c.Path(), taskId)
	if err != nil {
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	created, createErr := tasks.Registry.SetWithKeyAndBilling(taskId, "PENDING", 0, "", "Preparing R2 OCR job...", userID, reservation.ID, string(billing.ReservationKindDatabase))
	if createErr != nil || !created {
		_ = billing.Default.Release(reservation.ID)
		return c.Status(fiber.StatusServiceUnavailable).JSON(APIError{
			Code:    "TASK_STORAGE_UNAVAILABLE",
			Message: "The task could not be safely registered. Please try again.",
		})
	}
	if err := tasks.Registry.AdmitDurable(context.Background(), taskId, userID, reservation.ID, string(billing.ReservationKindDatabase)); err != nil {
		_ = billing.Default.Release(reservation.ID)
		return c.Status(fiber.StatusServiceUnavailable).JSON(APIError{
			Code:    "TASK_STORAGE_UNAVAILABLE",
			Message: "The task could not be safely registered. Please try again.",
		})
	}

	go func(id string, refs []R2ImageRef, reservationID, lang string) {
		taskCtx, taskCancel := context.WithCancel(context.Background())
		defer taskCancel()

		stopPoller := make(chan struct{})
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopPoller:
					return
				case <-taskCtx.Done():
					return
				case <-ticker.C:
					task, _ := tasks.Registry.Get(id)
					if task != nil && task.Status == "CANCELLED" {
						taskCancel()
						return
					}
				}
			}
		}()

		defer func() {
			close(stopPoller)
			// Uploaded references may be reused by another live job. Their
			// upload/session retention authority owns reclamation; this task
			// must not delete shared inputs merely because its goroutine ends.

			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				tasks.Registry.Set(id, "FAILED", 0, "", "Unexpected worker crash while building OCR PDF.")
				log.Printf("[OCR R2 JOB] panic: %v", r)
			}
		}()

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		tasks.Registry.Set(id, "PROCESSING", 30, "Downloading images from R2 and generating searchable PDF...", "")

		outPath, err := ctrl.service.ImageToTextPDFFromR2(taskCtx, refs, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			if taskCtx.Err() == nil {
				tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			}
			return
		}

		if _, err := uploads.CheckPDFPageLimit(outPath, "MAX_PAGES_OCR", 150); err != nil {
			_ = billing.Default.Release(reservationID)
			if taskCtx.Err() == nil {
				tasks.Registry.Set(id, "FAILED", 0, "", "The image conversion service did not produce a valid PDF.")
			}
			_ = os.Remove(outPath)
			return
		}

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			_ = os.Remove(outPath)
			return
		}

		defer os.Remove(outPath)
		key := "outputs/tasks/" + id + "/compiled.pdf"
		if err := tasks.Registry.PublishArtifact(context.Background(), id, key, outPath, "application/pdf"); err != nil {
			log.Printf("[OCR R2 JOB] completion %s needs reconciliation: %v", id, err)
		}

	}(taskId, req.Files, reservation.ID, lang)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"taskId": taskId,
	})
}
