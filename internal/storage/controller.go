package storage

import (
	"bytes"
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const maxLocalOCRUploadBytes = 100 * 1024 * 1024

func (ctrl *Controller) PresignUploads(c *fiber.Ctx) error {
	var req PresignRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_R2_PRESIGN_REQUEST",
			Message: "Invalid presign request body: " + err.Error(),
		})
	}

	if len(req.Files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "No files provided for presigning.",
		})
	}

	owner, _ := c.Locals("user_id").(string)
	var err error
	req, err = prepareOwnedUploads(req, owner)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{Code: "INVALID_UPLOAD_SCOPE", Message: err.Error()})
	}
	// Preserve the existing owner-scoped object-key contract while routing only
	// local-development searchable-PDF image uploads through the filesystem
	// adapter shared with the Worker. Managed environments always select R2.
	if !RemoteStorageEnabled() && req.Purpose == "image_to_text_pdf" {
		items := make([]PresignItem, 0, len(req.Files))
		for _, file := range req.Files {
			if file.Size <= 0 || file.Size > maxLocalOCRUploadBytes || !isLocalOCRImageType(file.Type) {
				return c.Status(fiber.StatusBadRequest).JSON(APIError{
					Code:    "INVALID_LOCAL_OCR_UPLOAD",
					Message: "Local searchable-PDF uploads require JPG, PNG, or WebP images up to 100 MiB each.",
				})
			}
			query := url.Values{}
			query.Set("key", file.Key)
			query.Set("size", strconv.FormatInt(file.Size, 10))
			items = append(items, PresignItem{
				Name:            file.Name,
				Size:            file.Size,
				Type:            file.Type,
				Key:             file.Key,
				UploadURL:       strings.TrimRight(c.BaseURL(), "/") + "/api/storage/local-ocr-image?" + query.Encode(),
				Method:          "PUT",
				WithCredentials: true,
			})
		}
		return c.JSON(PresignResponse{Files: items})
	}

	store, err := Default()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "R2_NOT_CONFIGURED",
			Message: err.Error(),
		})
	}

	resp, err := store.PresignUploads(context.Background(), req, 15*time.Minute)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "R2_PRESIGN_FAILED",
			Message: err.Error(),
		})
	}

	return c.JSON(resp)
}

func (ctrl *Controller) UploadLocalOCRImage(c *fiber.Ctx) error {
	if RemoteStorageEnabled() {
		return c.SendStatus(fiber.StatusNotFound)
	}
	owner, _ := c.Locals("user_id").(string)
	key := c.Query("key")
	if !IsOwnedKey(key, owner, "image_to_text_pdf") {
		return c.Status(fiber.StatusForbidden).JSON(APIError{Code: "UPLOAD_NOT_OWNED", Message: "Upload is not authorized."})
	}

	expectedSize, err := strconv.ParseInt(c.Query("size"), 10, 64)
	contentLength := int64(c.Context().Request.Header.ContentLength())
	if err != nil || expectedSize <= 0 || expectedSize > maxLocalOCRUploadBytes || contentLength != expectedSize {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(APIError{Code: "INVALID_LOCAL_UPLOAD_SIZE", Message: "Upload size does not match its authorized size."})
	}
	if !isLocalOCRImageType(c.Get(fiber.HeaderContentType)) {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(APIError{Code: "INVALID_LOCAL_OCR_MEDIA_TYPE", Message: "Only JPG, PNG, or WebP images are accepted."})
	}
	body := c.Body()
	if int64(len(body)) != expectedSize {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{Code: "LOCAL_UPLOAD_LENGTH_MISMATCH", Message: "Upload body length did not match its authorized size."})
	}
	if _, _, err := SaveLocalStream(context.Background(), key, bytes.NewReader(body)); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{Code: "LOCAL_UPLOAD_FAILED", Message: "Could not persist the local searchable-PDF input."})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func isLocalOCRImageType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0])) {
	case "image/jpeg", "image/jpg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}
