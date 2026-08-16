package ocr

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/disk"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/temp"
	"pdfnest-backend/internal/uploads"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) ProcessOCR(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF file upload parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_OCR", 150); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	lang := c.FormValue("lang", "eng")

	// Monitor client disconnection to cancel backend & worker operations immediately
	reqCtx := c.Context()
	ctx, cancel := context.WithCancel(c.UserContext())
	defer cancel()

	go func() {
		select {
		case <-reqCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	outputPath, err := ctrl.service.ExtractTextFromPDF(ctx, upload.Path, lang)
	if err != nil {
		if ctx.Err() != nil {
			return c.Status(499).JSON(APIError{
				Code:    "CLIENT_CLOSED_REQUEST",
				Message: "Client closed request during OCR processing.",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OCR_PROCESSING_FAILED",
			Message: "OCR extraction failed: " + err.Error(),
		})
	}
	defer func() {
		if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary output data file: %v", err)
		}
	}()

	c.Set("Content-Type", "text/plain")
	c.Attachment(filepath.Base(outputPath))
	return c.SendFile(outputPath)
}

func (ctrl *Controller) ProcessImageToTextPDF(c *fiber.Ctx) error {
	files, err := uploads.MustFiles(c, "images")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_IMAGE_DATASET",
			Message: "No valid file matrices array detected within the 'images' field required for text processing compilation.",
		})
	}

	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_IMAGE_DATASET",
			Message: "No valid file matrices array detected within the 'images' field required for text processing compilation.",
		})
	}

	maxImages := uploads.GetEnvInt("MAX_PAGES_OCR", 150)
	if len(files) > maxImages {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: fmt.Sprintf("Number of uploaded images (%d) exceeds maximum allowed limit of %d images for OCR operations.", len(files), maxImages),
		})
	}

	lang := c.FormValue("lang", "eng")

	temporaryImagePaths := make([]string, 0, len(files))
	for _, f := range files {
		temporaryImagePaths = append(temporaryImagePaths, f.Path)
	}

	outputPath, err := ctrl.service.ImageToTextPDF(c.UserContext(), temporaryImagePaths, lang)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "SEARCHABLE_PDF_COMPILATION_FAILED",
			Message: "OCR image compilation failed: " + err.Error(),
		})
	}
	defer func() {
		if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Failed to delete temporary output data file: %v", err)
		}
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment("ocr_processed_document.pdf")
	return c.SendFile(outputPath)
}

func (ctrl *Controller) HandleAsyncExtractText(c *fiber.Ctx) error {
	// Determine user ID: prefer authenticated user ID, fallback to identity ID for guests.
	var userID string
	if uid, ok := c.Locals(identity.LocalUserIDKey).(string); ok && uid != "" {
		userID = uid
	} else if iid, ok := c.Locals(identity.LocalIdentityIDKey).(string); ok && iid != "" {
		userID = iid
	} else {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{Code: "IDENTITY_MISSING", Message: "Unable to determine user identity"})
	}
	lang := c.FormValue("lang", "eng")

	ownerIdentity, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if ownerIdentity == "" {
		ownerIdentity = c.IP()
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "MISSING_FILE", Message: "No file uploaded"})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_OCR", 150); err != nil {
		return c.Status(400).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	taskId := uuid.New().String()

	pages, images, err := billing.EstimateFromRequest(c, billing.ExtractTextPDF)
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(400).JSON(APIError{Code: "ESTIMATE_ERR", Message: err.Error()})
	}

	reservation, err := billing.Default.ReserveWithTaskID(userID, billing.ExtractTextPDF, pages, images, c.Path(), taskId)
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	requiredBytes := disk.EstimateRequiredSpace(upload.Header.Size, 3.0, 100*1024*1024)
	if diskErr := disk.CheckAvailableSpace(temp.GetDir(), requiredBytes); diskErr != nil {
		_ = billing.Default.Release(reservation.ID)
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInsufficientStorage).JSON(APIError{
			Code:    "INSUFFICIENT_STORAGE",
			Message: "Insufficient server disk space available to start OCR extraction operation.",
		})
	}

	inputPath := filepath.Join(temp.GetDir(), taskId+"-"+filepath.Base(upload.Header.Filename))
	if err := copyFile(upload.Path, inputPath); err != nil {
		_ = billing.Default.Release(reservation.ID)
		idempotency.Release(c, nil)
		return c.Status(500).JSON(APIError{Code: "DISK_ERR", Message: "Failed to write workspace data cache"})
	}

	okCreated, err := tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Initializing Document Ingestion Matrix...", ownerIdentity, reservation.ID)
	if err != nil || !okCreated {
		_ = billing.Default.Release(reservation.ID)
		_ = os.Remove(inputPath)
		idempotency.Release(c, nil)
		return c.Status(500).JSON(APIError{Code: "TASK_ERR", Message: "Failed to register task"})
	}

	_ = idempotency.SetTaskID(c, taskId, nil)

	go func(id, srcPath, reservationID, lang, owner string) {
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
						log.Printf("[FORENSIC %s] Context Cancelled for task %s", time.Now().UTC().Format(time.RFC3339Nano), id)
						taskCancel()
						return
					}
				}
			}
		}()

		var localOutPath string
		var releaseToken func()

		defer func() {
			close(stopPoller)
			if releaseToken != nil {
				releaseToken()
			}
			_ = os.Remove(srcPath)
			if localOutPath != "" {
				_ = os.Remove(localOutPath)
			}
			log.Printf("[FORENSIC %s] Backend Cleanup Completed for task %s", time.Now().UTC().Format(time.RFC3339Nano), id)
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Subprocess thread failure occurred.", owner)
			}
		}()

		var acquired bool
		for attempt := 0; attempt < 30; attempt++ {
			if taskCtx.Err() != nil {
				_ = billing.Default.Release(reservationID)
				return
			}
			acqCtx, cancel := context.WithTimeout(taskCtx, 5*time.Second)
			rel, ok, acqErr := limiter.Default.AcquireWithRelease(acqCtx, id, owner)
			cancel()

			if acqErr != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Execution capacity service unavailable.", owner)
				return
			}
			if ok {
				acquired = true
				releaseToken = rel
				break
			}
			time.Sleep(1 * time.Second)
		}

		if !acquired {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Server capacity reached. Task execution timed out waiting for capacity.", owner)
			return
		}

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 35, "", "Running OCR and creating searchable text...", owner)

		outPath, err := ctrl.service.ExtractTextFromPDF(taskCtx, srcPath, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			if taskCtx.Err() == nil {
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			}
			return
		}
		localOutPath = outPath

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		r2Key := fmt.Sprintf("outputs/tasks/%s/%s", id, filepath.Base(outPath))
		r2Store, r2Err := storage.Default()
		isProd := os.Getenv("APP_ENV") == "production"

		if r2Err != nil || r2Store == nil {
			if isProd {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Cloud storage is unconfigured in production environment.", owner)
				return
			}
			if err := billing.Default.Commit(reservationID); err != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
				return
			}
			_, _ = tasks.Registry.SetWithKey(id, "COMPLETED", 100, outPath, "", owner)
			return
		}

		if err := r2Store.UploadFile(outPath, r2Key, "text/plain; charset=utf-8"); err != nil {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Failed to save completed document to cloud storage.", owner)
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
			return
		}

		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			log.Printf("[OCR TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, inputPath, reservation.ID, lang, ownerIdentity)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) HandleAsyncImageToTextPDF(c *fiber.Ctx) error {
	// Determine user ID: prefer authenticated user ID, fallback to identity ID for guests.
	var userID string
	if uid, ok := c.Locals(identity.LocalUserIDKey).(string); ok && uid != "" {
		userID = uid
	} else if iid, ok := c.Locals(identity.LocalIdentityIDKey).(string); ok && iid != "" {
		userID = iid
	} else {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{Code: "IDENTITY_MISSING", Message: "Unable to determine user identity"})
	}
	lang := c.FormValue("lang", "eng")

	ownerIdentity, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if ownerIdentity == "" {
		ownerIdentity = c.IP()
	}

	files, err := uploads.MustFiles(c, "images")
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_FORM", Message: "Form structure processing error"})
	}

	if len(files) == 0 {
		return c.Status(400).JSON(APIError{Code: "MISSING_IMAGES", Message: "No file targets dropped inside body array"})
	}

	maxImages := uploads.GetEnvInt("MAX_PAGES_OCR", 150)
	if len(files) > maxImages {
		return c.Status(400).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: fmt.Sprintf("Number of uploaded images (%d) exceeds maximum allowed limit of %d images for OCR operations.", len(files), maxImages),
		})
	}

	taskId := uuid.New().String()

	tempPaths := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(temp.GetDir(), uuid.New().String()+"-"+filepath.Base(f.Header.Filename))
		if err := copyFile(f.Path, path); err == nil {
			tempPaths = append(tempPaths, path)
		}
	}

	pages, images, err := billing.EstimateFromRequest(c, billing.ImageToTextPDF)
	if err != nil {
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		idempotency.Release(c, nil)
		return c.Status(400).JSON(APIError{Code: "ESTIMATE_ERR", Message: err.Error()})
	}

	reservation, err := billing.Default.ReserveWithTaskID(userID, billing.ImageToTextPDF, pages, images, c.Path(), taskId)
	if err != nil {
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	var totalInputSize int64
	for _, f := range files {
		totalInputSize += f.Header.Size
	}
	requiredBytes := disk.EstimateRequiredSpace(totalInputSize, 3.0, 100*1024*1024)
	if diskErr := disk.CheckAvailableSpace(temp.GetDir(), requiredBytes); diskErr != nil {
		_ = billing.Default.Release(reservation.ID)
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		idempotency.Release(c, nil)
		return c.Status(fiber.StatusInsufficientStorage).JSON(APIError{
			Code:    "INSUFFICIENT_STORAGE",
			Message: "Insufficient server disk space available to start OCR rendering operation.",
		})
	}

	okCreated, err := tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Allocating compilation environment nodes...", ownerIdentity, reservation.ID)
	if err != nil || !okCreated {
		_ = billing.Default.Release(reservation.ID)
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		idempotency.Release(c, nil)
		return c.Status(500).JSON(APIError{Code: "TASK_ERR", Message: "Failed to register task"})
	}

	_ = idempotency.SetTaskID(c, taskId, nil)

	go func(id string, imgPaths []string, reservationID, lang, owner string) {
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

		var localOutPath string
		var releaseToken func()

		defer func() {
			close(stopPoller)
			if releaseToken != nil {
				releaseToken()
			}
			for _, p := range imgPaths {
				_ = os.Remove(p)
			}
			if localOutPath != "" {
				_ = os.Remove(localOutPath)
			}
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Subprocess matrix generation fault.", owner)
			}
		}()

		var acquired bool
		for attempt := 0; attempt < 30; attempt++ {
			if taskCtx.Err() != nil {
				_ = billing.Default.Release(reservationID)
				return
			}
			acqCtx, cancel := context.WithTimeout(taskCtx, 5*time.Second)
			rel, ok, acqErr := limiter.Default.AcquireWithRelease(acqCtx, id, owner)
			cancel()

			if acqErr != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Execution capacity service unavailable.", owner)
				return
			}
			if ok {
				acquired = true
				releaseToken = rel
				break
			}
			time.Sleep(1 * time.Second)
		}

		if !acquired {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Server capacity reached. Task execution timed out waiting for capacity.", owner)
			return
		}

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 35, "", "Scanning character grid topologies and building PDF layout layers...", owner)

		outPath, err := ctrl.service.ImageToTextPDF(taskCtx, imgPaths, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			if taskCtx.Err() == nil {
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			}
			return
		}
		localOutPath = outPath

		if taskCtx.Err() != nil {
			_ = billing.Default.Release(reservationID)
			return
		}

		r2Key := fmt.Sprintf("outputs/tasks/%s/compiled.pdf", id)
		r2Store, r2Err := storage.Default()
		isProd := os.Getenv("APP_ENV") == "production"

		if r2Err != nil || r2Store == nil {
			if isProd {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Cloud storage is unconfigured in production environment.", owner)
				return
			}
			if err := billing.Default.Commit(reservationID); err != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
				return
			}
			_, _ = tasks.Registry.SetWithKey(id, "COMPLETED", 100, outPath, "", owner)
			return
		}

		if err := r2Store.UploadFile(outPath, r2Key, "application/pdf"); err != nil {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Failed to save completed document to cloud storage.", owner)
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
			return
		}

		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			_ = r2Store.DeleteObject(context.Background(), r2Key)
			log.Printf("[OCR TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, tempPaths, reservation.ID, lang, ownerIdentity)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) Languages(c *fiber.Ctx) error {
	langs, err := GetOCRLanguages()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OCR_LANGUAGES_FAILED",
			Message: err.Error(),
		})
	}

	return c.JSON(langs)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}
