package ocr

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/uploads"

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

	outputPath, err := ctrl.service.ExtractTextFromPDF(upload.Path, lang)
	if err != nil {
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

	outputPath, err := ctrl.service.ImageToTextPDF(temporaryImagePaths, lang)
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
	userID := c.Locals("user_id").(string)
	lang := c.FormValue("lang", "eng")

	ownerIdentity, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if ownerIdentity == "" {
		ownerIdentity = c.IP()
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		idempotency.Release(c, nil)
		return c.Status(400).JSON(APIError{Code: "MISSING_FILE", Message: "No file uploaded"})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_OCR", 150); err != nil {
		idempotency.Release(c, nil)
		return c.Status(400).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	taskId := uuid.New().String()
	_, _ = tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Initializing Document Ingestion Matrix...", ownerIdentity)
	_ = idempotency.SetTaskID(c, taskId, nil)

	inputPath := filepath.Join(os.TempDir(), taskId+"-"+filepath.Base(upload.Header.Filename))
	if err := copyFile(upload.Path, inputPath); err != nil {
		idempotency.Release(c, nil)
		return c.Status(500).JSON(APIError{Code: "DISK_ERR", Message: "Failed to write workspace data cache"})
	}

	release, ok := limiter.Default.TryAcquire()
	if !ok {
		c.Set("Retry-After", "5")
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "SERVER_BUSY",
			Message: "Server processing capacity reached. Please try again in a few seconds.",
		})
	}

	reservation, err := billing.ReserveFromRequest(c, userID, billing.ExtractTextPDF)
	if err != nil {
		release()
		_ = os.Remove(inputPath)
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	go func(id, srcPath, reservationID, lang, owner string, releaseToken func()) {
		var localOutPath string
		defer func() {
			releaseToken()
			_ = os.Remove(srcPath)
			if localOutPath != "" {
				_ = os.Remove(localOutPath)
			}
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Subprocess thread failure occurred.", owner)
			}
		}()

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 35, "", "Running OCR and creating searchable text...", owner)

		outPath, err := ctrl.service.ExtractTextFromPDF(srcPath, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			return
		}
		localOutPath = outPath

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
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
			return
		}

		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			log.Printf("[OCR TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, inputPath, reservation.ID, lang, ownerIdentity, release)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) HandleAsyncImageToTextPDF(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)
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
	_, _ = tasks.Registry.SetWithKey(taskId, "PENDING", 0, "", "Allocating compilation environment nodes...", ownerIdentity)
	_ = idempotency.SetTaskID(c, taskId, nil)

	tempPaths := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(os.TempDir(), uuid.New().String()+"-"+filepath.Base(f.Header.Filename))
		if err := copyFile(f.Path, path); err == nil {
			tempPaths = append(tempPaths, path)
		}
	}

	release, ok := limiter.Default.TryAcquire()
	if !ok {
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		c.Set("Retry-After", "5")
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "SERVER_BUSY",
			Message: "Server processing capacity reached. Please try again in a few seconds.",
		})
	}

	reservation, err := billing.ReserveFromRequest(c, userID, billing.ImageToTextPDF)
	if err != nil {
		release()
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	go func(id string, imgPaths []string, reservationID, lang, owner string, releaseToken func()) {
		var localOutPath string
		defer func() {
			releaseToken()
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

		_, _ = tasks.Registry.SetWithKey(id, "PROCESSING", 35, "", "Scanning character grid topologies and building PDF layout layers...", owner)

		outPath, err := ctrl.service.ImageToTextPDF(imgPaths, lang)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", err.Error(), owner)
			return
		}
		localOutPath = outPath

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
			_, _ = tasks.Registry.SetWithKey(id, "FAILED", 0, "", "Billing finalization failed", owner)
			return
		}

		accepted, _ := tasks.Registry.SetWithKey(id, "COMPLETED", 100, r2Key, "", owner)
		if !accepted {
			log.Printf("[OCR TASK] SetWithKey COMPLETED rejected for task %s (status already terminal)", id)
		}
	}(taskId, tempPaths, reservation.ID, lang, ownerIdentity, release)

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
