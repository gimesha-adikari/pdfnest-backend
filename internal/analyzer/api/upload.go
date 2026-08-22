package api

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"pdfnest-backend/internal/storage"
)

// UploadArchiveResponse returns the canonical storage metadata of the persisted archive.
type UploadArchiveResponse struct {
	StorageKey     string `json:"storageKey"`
	FileName       string `json:"fileName"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	RepositoryName string `json:"repositoryName"`
}

// UploadArchive handles POST /api/v1/analyzer/upload for atomic ZIP and bundled directory uploads.
func (ctrl *Controller) UploadArchive(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "UPLOAD_FAILED",
			Message: "No file provided in multipart upload: " + err.Error(),
		})
	}

	// 1. Enforce archive byte limits (250MB)
	const maxArchiveBytes = int64(250 * 1024 * 1024)
	if fileHeader.Size > maxArchiveBytes {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(APIError{
			Code:    "ARCHIVE_TOO_LARGE",
			Message: fmt.Sprintf("Archive size (%d bytes) exceeds the maximum allowed limit of 250MB", fileHeader.Size),
		})
	}

	file, err := fileHeader.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "UPLOAD_FAILED",
			Message: "Failed to open uploaded file: " + err.Error(),
		})
	}
	defer file.Close()

	// 2. Validate ZIP Magic Header (PK\x03\x04)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(file, magic); err != nil || string(magic) != "PK\x03\x04" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_ARCHIVE",
			Message: "Uploaded file is not a valid ZIP archive format.",
		})
	}

	// Recombine reader to include magic bytes
	stream := io.MultiReader(bytes.NewReader(magic), file)

	// 3. Build Canonical Storage Key
	cleanName := filepath.Base(fileHeader.Filename)
	cleanName = strings.TrimSpace(cleanName)
	if cleanName == "" || cleanName == "." {
		cleanName = "repository.zip"
	}
	if !strings.HasSuffix(strings.ToLower(cleanName), ".zip") {
		cleanName += ".zip"
	}

	storageKey := fmt.Sprintf("repositories/raw/%d_%s-%s", time.Now().UnixNano(), uuid.NewString()[:8], cleanName)

	// 4. Persist to storage backend
	written, sha256Hex, err := storage.SaveLocalStream(c.Context(), storageKey, stream)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "UPLOAD_FAILED",
			Message: "Failed to persist archive to storage: " + err.Error(),
		})
	}

	repoName := strings.TrimSuffix(cleanName, ".zip")
	if rName := c.FormValue("repositoryName"); strings.TrimSpace(rName) != "" {
		repoName = strings.TrimSpace(rName)
	}

	return c.Status(fiber.StatusCreated).JSON(UploadArchiveResponse{
		StorageKey:     storageKey,
		FileName:       cleanName,
		SHA256:         sha256Hex,
		Size:           written,
		RepositoryName: repoName,
	})
}
