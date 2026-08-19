package conversion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/uploads"
	"pdfnest-backend/internal/worker"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func (ctrl *Controller) HandleAsyncPDFToMarkdown(c *fiber.Ctx) error {
	var userID string
	if uid, ok := c.Locals(identity.LocalUserIDKey).(string); ok && uid != "" {
		userID = uid
	} else if iid, ok := c.Locals(identity.LocalIdentityIDKey).(string); ok && iid != "" {
		userID = iid
	} else {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "IDENTITY_MISSING",
			"message": "Unable to determine user identity",
		})
	}

	ownerIdentity, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if ownerIdentity == "" {
		ownerIdentity = c.IP()
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_FILE",
			"message": "No valid PDF file uploaded",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_CONVERT", 150); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "PAGE_LIMIT_EXCEEDED",
			"message": err.Error(),
		})
	}

	lang := c.FormValue("lang", "eng")
	filePassword := c.FormValue("password")
	if filePassword == "" {
		filePassword = c.FormValue("file_password")
	}
	includeAnnotations := strings.ToLower(c.FormValue("include_annotations")) == "true"
	embedImages := strings.ToLower(c.FormValue("embed_images")) == "true"

	taskId := uuid.New().String()

	pages, images, err := billing.EstimateFromRequest(c, billing.ConvertPDFToMarkdown)
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "ESTIMATE_ERR",
			"message": err.Error(),
		})
	}

	identityType, _ := c.Locals(identity.LocalIdentityType).(string)
	var reservationID string

	if identityType == string(identity.TypeGuest) {
		if billing.GuestQuota == nil {
			idempotency.Release(c, nil)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"code":    "CONFIG_ERR",
				"message": "Guest quota service unavailable",
			})
		}
		ctx := identity.RequestContext(c)
		gres, err := billing.GuestQuota.Reserve(ctx, ownerIdentity, billing.ConvertPDFToMarkdown, pages, images, c.Path())
		if err != nil {
			idempotency.Release(c, nil)
			var berr *billing.BillingError
			if errors.As(err, &berr) {
				berr.Tool = billing.ConvertPDFToMarkdown.Name
				return c.Status(fiber.StatusTooManyRequests).JSON(berr)
			}
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"code":    "QUOTA_EXCEEDED",
				"message": err.Error(),
			})
		}
		reservationID = gres.ID
	} else {
		res, err := billing.Default.ReserveWithTaskID(userID, billing.ConvertPDFToMarkdown, pages, images, c.Path(), taskId)
		if err != nil {
			idempotency.Release(c, nil)
			var berr *billing.BillingError
			if errors.As(err, &berr) {
				berr.Tool = billing.ConvertPDFToMarkdown.Name
				return c.Status(fiber.StatusTooManyRequests).JSON(berr)
			}
			return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
				"code":    "QUOTA_EXCEEDED",
				"message": err.Error(),
			})
		}
		reservationID = res.ID
	}

	r2Store, err := storage.Default()
	if err != nil {
		if identityType == string(identity.TypeGuest) {
			_ = billing.GuestQuota.Release(identity.RequestContext(c), reservationID)
		} else {
			_ = billing.Default.Release(reservationID)
		}
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "STORAGE_INIT_ERR",
			"message": "Failed to initialize cloud storage",
		})
	}

	sourceKey := fmt.Sprintf("jobs/markdown/source/%s.pdf", taskId)
	if err := r2Store.UploadFile(upload.Path, sourceKey, "application/pdf"); err != nil {
		if identityType == string(identity.TypeGuest) {
			_ = billing.GuestQuota.Release(identity.RequestContext(c), reservationID)
		} else {
			_ = billing.Default.Release(reservationID)
		}
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "STORAGE_UPLOAD_ERR",
			"message": "Failed to upload encrypted file payload: " + err.Error(),
		})
	}

	downloadToken := uuid.New().String()
	_, _ = tasks.Registry.SetWithDownloadToken(taskId, "QUEUED", 0, "", "PDF to Markdown job queued", ownerIdentity, downloadToken, reservationID)

	payload := map[string]interface{}{
		"actor_name": "pdf_to_markdown_job",
		"args": []interface{}{
			taskId,
			sourceKey,
			filePassword,
			lang,
			includeAnnotations,
			embedImages,
			filepath.Base(upload.Path),
		},
		"kwargs": map[string]interface{}{},
	}

	releaseReservation := func() {
		if identityType == string(identity.TypeGuest) {
			if billing.GuestQuota != nil {
				_ = billing.GuestQuota.Release(identity.RequestContext(c), reservationID)
			}
		} else {
			_ = billing.Default.Release(reservationID)
		}
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		releaseReservation()
		tasks.Registry.Set(taskId, "FAILED", 0, "", "Failed to encode worker payload")
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "PAYLOAD_ERR",
			"message": err.Error(),
		})
	}

	workerSubmitURL := worker.GetWorkerURL() + "/api/v1/jobs/submit"
	req, err := http.NewRequestWithContext(c.UserContext(), http.MethodPost, workerSubmitURL, bytes.NewReader(jsonBytes))
	if err != nil {
		releaseReservation()
		tasks.Registry.Set(taskId, "FAILED", 0, "", "Failed to create worker request")
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "REQUEST_ERR",
			"message": err.Error(),
		})
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := worker.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		releaseReservation()
		tasks.Registry.Set(taskId, "FAILED", 0, "", "Failed to submit job to worker")
		idempotency.Release(c, nil)
		errMsg := "Failed to submit job to worker"
		if err != nil {
			errMsg += ": " + err.Error()
		} else if resp != nil {
			errMsg += fmt.Sprintf(" (HTTP %d)", resp.StatusCode)
		}
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"code":    "WORKER_DISPATCH_ERR",
			"message": errMsg,
		})
	}
	defer resp.Body.Close()

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"success":       true,
		"job_id":        taskId,
		"task_id":       taskId,
		"status":        "QUEUED",
		"downloadToken": downloadToken,
	})
}
