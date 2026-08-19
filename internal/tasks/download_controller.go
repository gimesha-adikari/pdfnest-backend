package tasks

import (
	"mime"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"strings"

	"github.com/gofiber/fiber/v2"
)

func HandleTaskDownload(c *fiber.Ctx) error {
	id := c.Params("id")
	task, err := Registry.Get(id)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable.",
		})
	}

	if task == nil || task.Status != "COMPLETED" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"code":    "FILE_NOT_FOUND",
			"message": "The requested asset is either expired or still processing.",
		})
	}

	// Task output is scoped to the identity that created the task.
	requesterID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if requesterID == "" {
		requesterID = c.IP()
	}
	if task.OwnerIdentity != "" && !isAuthorizedOwner(c, task, requesterID) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"code":    "FORBIDDEN",
			"message": "You are not authorized to download this task artifact.",
		})
	}

	if task.ReservationID != "" && CommitTaskBillingHandler != nil {
		CommitTaskBillingHandler(task.ReservationID)
	}

	key := task.ResultKey
	if key == "" && strings.HasPrefix(task.ResultURL, "r2://") {
		key = strings.TrimPrefix(task.ResultURL, "r2://")
	}

	if key != "" {
		r2Store, err := storage.Default()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"code":    "STORAGE_ERROR",
				"message": "Failed to initialize cloud storage client.",
			})
		}

		ext := filepath.Ext(key)
		tmpPath, err := r2Store.DownloadToTemp(key, "dl-task-", ext)
		if err != nil {
			return c.Status(fiber.StatusGone).JSON(fiber.Map{
				"code":    "ASSET_REMOVED",
				"message": "The requested asset has expired or is no longer available in cloud storage.",
			})
		}
		defer func() {
			_ = os.Remove(tmpPath)
		}()

		// The object key is server-controlled, so its extension is safe for content-type selection.
		contentType := "application/octet-stream"
		switch strings.ToLower(ext) {
		case ".pdf":
			contentType = "application/pdf"
		case ".txt":
			contentType = "text/plain; charset=utf-8"
		case ".md":
			contentType = "text/markdown; charset=utf-8"
		case ".json":
			contentType = "application/json"
		default:
			if customType := mime.TypeByExtension(ext); customType != "" {
				contentType = customType
			}
		}

		c.Set("Content-Type", contentType)
		c.Attachment(filepath.Base(key))
		return c.SendFile(tmpPath)
	}

	// Retain the local fallback while tasks created before object storage migration can expire.
	filePath := task.ResultURL
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"code":    "ASSET_REMOVED",
			"message": "The requested temporary asset has been cleaned from cache storage.",
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Attachment(filepath.Base(filePath))
	return c.SendFile(filePath)
}

func isAuthorizedOwner(c *fiber.Ctx, task *TaskStatus, requesterID string) bool {
	if task.OwnerIdentity == "" {
		return true
	}
	if task.OwnerIdentity == requesterID {
		return true
	}

	// Capability download token check for clients where session cookie was dropped/blocked
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		token = strings.TrimSpace(c.Get("X-Download-Token"))
	}
	if token != "" && task.DownloadToken != "" && token == task.DownloadToken {
		return true
	}

	return false
}
