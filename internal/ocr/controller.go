package ocr

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/billing"
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
	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF file upload parameter.",
		})
	}

	outputPath, err := ctrl.service.ExtractTextFromPDF(upload.Path)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "OCR_PROCESSING_FAILED",
			Message: "Tesseract engine pipeline failed extraction process: " + err.Error(),
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

	temporaryImagePaths := make([]string, 0, len(files))
	for _, f := range files {
		temporaryImagePaths = append(temporaryImagePaths, f.Path)
	}

	outputPath, err := ctrl.service.ImageToTextPDF(temporaryImagePaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "SEARCHABLE_PDF_COMPILATION_FAILED",
			Message: "Smart image extraction pipeline failure: " + err.Error(),
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

	upload, err := uploads.MustFile(c, "file")
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "MISSING_FILE", Message: "No file uploaded"})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Initializing Document Ingestion Matrix...", "")

	inputPath := filepath.Join(os.TempDir(), taskId+"-"+filepath.Base(upload.Header.Filename))
	if err := copyFile(upload.Path, inputPath); err != nil {
		return c.Status(500).JSON(APIError{Code: "DISK_ERR", Message: "Failed to write workspace data cache"})
	}

	reservation, err := billing.ReserveFromRequest(c, userID, billing.ExtractTextPDF)
	if err != nil {
		_ = os.Remove(inputPath)
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	go func(id, srcPath, reservationID string) {
		defer func() {
			_ = os.Remove(srcPath)
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				tasks.Registry.Set(id, "FAILED", 0, "", "Subprocess thread failure occurred.")
			}
		}()

		tasks.Registry.Set(
			id,
			"PROCESSING",
			35,
			"Running OCR and creating searchable PDF...",
			"",
		)

		outPath, err := ctrl.service.ExtractTextFromPDF(srcPath)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", "Billing finalization failed")
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")
	}(taskId, inputPath, reservation.ID)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
}

func (ctrl *Controller) HandleAsyncImageToTextPDF(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	files, err := uploads.MustFiles(c, "images")
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_FORM", Message: "Form structure processing error"})
	}

	if len(files) == 0 {
		return c.Status(400).JSON(APIError{Code: "MISSING_IMAGES", Message: "No file targets dropped inside body array"})
	}

	taskId := uuid.New().String()
	tasks.Registry.Set(taskId, "PENDING", 0, "Allocating compilation environment nodes...", "")

	tempPaths := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(os.TempDir(), uuid.New().String()+"-"+filepath.Base(f.Header.Filename))
		if err := copyFile(f.Path, path); err == nil {
			tempPaths = append(tempPaths, path)
		}
	}

	reservation, err := billing.ReserveFromRequest(c, userID, billing.ImageToTextPDF)
	if err != nil {
		for _, p := range tempPaths {
			_ = os.Remove(p)
		}
		return c.Status(fiber.StatusTooManyRequests).JSON(APIError{
			Code:    "BILLING_BLOCKED",
			Message: err.Error(),
		})
	}

	go func(id string, imgPaths []string, reservationID string) {
		defer func() {
			for _, p := range imgPaths {
				_ = os.Remove(p)
			}
			if r := recover(); r != nil {
				_ = billing.Default.Release(reservationID)
				tasks.Registry.Set(id, "FAILED", 0, "", "Subprocess matrix generation fault.")
			}
		}()

		tasks.Registry.Set(id, "PROCESSING", 35, "Scanning character grid topologies and building PDF layout layers...", "")

		outPath, err := ctrl.service.ImageToTextPDF(imgPaths)
		if err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", err.Error())
			return
		}

		if err := billing.Default.Commit(reservationID); err != nil {
			_ = billing.Default.Release(reservationID)
			tasks.Registry.Set(id, "FAILED", 0, "", "Billing finalization failed")
			return
		}

		tasks.Registry.Set(id, "COMPLETED", 100, outPath, "")
	}(taskId, tempPaths, reservation.ID)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskId})
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
