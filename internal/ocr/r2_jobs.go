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

	_, _ = tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Preparing R2 OCR job...", userID, reservation.ID)

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
			store, err := storage.Default()
			if err == nil {
				ctx := context.Background()
				for _, ref := range refs {
					if ref.Key != "" {
						_ = store.DeleteObject(ctx, ref.Key)
					}
				}
			} else {
				log.Printf("[OCR R2 JOB] Warning: Failed to init storage for cleanup: %v", err)
			}

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

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			_ = os.Remove(outPath)
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			_ = os.Remove(outPath)
			tasks.Registry.Set(id, "FAILED", 0, "", "Billing finalization failed")
			return
		}

		_ = tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")
	}(taskId, req.Files, reservation.ID, lang)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"taskId": taskId,
	})
}
