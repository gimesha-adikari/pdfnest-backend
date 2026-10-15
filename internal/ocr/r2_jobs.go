package ocr

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

	registered, err := tasks.Registry.SetWithKeyAndBilling(taskId, "PENDING", 0, "", "Preparing R2 OCR job...", userID, reservation.ID, string(billing.ReservationKindDatabase))
	if err != nil || !registered {
		_ = billing.Default.Release(reservation.ID)
		for _, ref := range req.Files {
			if ref.Key != "" {
				_ = storage.DeleteObject(context.Background(), ref.Key)
			}
		}
		return c.Status(fiber.StatusServiceUnavailable).JSON(APIError{
			Code:    "TASK_STORAGE_UNAVAILABLE",
			Message: "The task could not be safely registered. Please try again.",
		})
	}

	go func(id string, refs []R2ImageRef, reservationID, lang string) {
		taskCtx, taskCancel := context.WithCancel(context.Background())
		defer taskCancel()
		var outputPath string
		var outputKey string
		var artifactPublished bool
		ownerIdentity := userID
		reservationKind := billing.ReservationKindDatabase
		setState := func(status string, progress int, resultKey string, resultURL string, message string) (bool, error) {
			if resultKey != "" {
				return tasks.Registry.SetWithKey(id, status, progress, resultKey, message, ownerIdentity, reservationID, string(reservationKind))
			}
			return tasks.Registry.SetWithResultURL(id, status, progress, resultURL, message, ownerIdentity, reservationID, string(reservationKind))
		}
		markFailed := func(message string) {
			_, _ = setState("FAILED", 0, "", "", message)
		}

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
			for _, ref := range refs {
				if ref.Key != "" {
					_ = storage.DeleteObject(context.Background(), ref.Key)
				}
			}
			if outputPath != "" {
				_ = os.Remove(outputPath)
			}
			if outputKey != "" && !artifactPublished {
				_ = storage.DeleteObject(context.Background(), outputKey)
			}
			if r := recover(); r != nil {
				_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
				markFailed("Image-to-PDF processing failed.")
				log.Printf("[OCR R2 JOB] worker panicked for task %s", id)
			}
		}()

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		accepted, stateErr := setState("PROCESSING", 30, "", "", "Downloading images and generating searchable PDF...")
		if stateErr != nil || !accepted {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			return
		}

		outPath, err := ctrl.service.ImageToTextPDFFromR2(taskCtx, refs, lang)
		if err != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			if taskCtx.Err() == nil {
				markFailed("Image-to-PDF processing failed.")
			}
			return
		}
		outputPath = outPath

		if _, err := uploads.CheckPDFPageLimit(outputPath, "MAX_PAGES_OCR", 150); err != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			if taskCtx.Err() == nil {
				markFailed("The image conversion service did not produce a valid PDF.")
			}
			return
		}

		if taskCtx.Err() != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			return
		}

		outputKey = fmt.Sprintf("outputs/tasks/%s/%s", id, filepath.Base(outputPath))
		if err := storage.SaveObjectFile(taskCtx, outputKey, outputPath, "application/pdf"); err != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			markFailed("The generated PDF could not be safely stored.")
			return
		}
		if taskCtx.Err() != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.ReleaseAsync(context.Background(), reservationID, reservationKind)
			markFailed("Billing finalization failed.")
			return
		}

		var resultURL string
		if !storage.RemoteStorageEnabled() {
			resultURL = filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(outputKey))
		}
		var completed bool
		if resultURL == "" {
			completed, err = setState("COMPLETED", 100, outputKey, "", "")
		} else {
			completed, err = setState("COMPLETED", 100, "", resultURL, "")
		}
		if err != nil || !completed {
			log.Printf("[OCR R2 JOB] completion was not accepted for task %s", id)
			return
		}
		artifactPublished = true
	}(taskId, req.Files, reservation.ID, lang)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"taskId": taskId,
	})
}
