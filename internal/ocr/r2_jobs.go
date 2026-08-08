package ocr

import (
	"context"
	"log"
	"os"
	"strings"

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

	// Billing uses the image count directly.
	reservation, err := billing.Default.ReserveWithTaskID(userID, billing.ImageToTextPDF, 0, len(req.Files), c.Path(), taskId)
	if err != nil {
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	_, _ = tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Preparing R2 OCR job...", userID, reservation.ID)

	go func(id string, refs []R2ImageRef, reservationID, lang string) {
		defer func() {
			// 1. CLEANUP R2 IMAGES
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

			// 2. Handle Panics gracefully
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				tasks.Registry.Set(id, "FAILED", 0, "", "Unexpected worker crash while building OCR PDF.")
				log.Printf("[OCR R2 JOB] panic: %v", r)
			}
		}()

		tasks.Registry.Set(id, "PROCESSING", 30, "Downloading images from R2 and generating searchable PDF...", "")

		outPath, err := ctrl.service.ImageToTextPDFFromR2(refs, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
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
