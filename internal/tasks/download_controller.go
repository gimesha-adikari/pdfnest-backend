package tasks

import (
	"os"
	"path/filepath"

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
