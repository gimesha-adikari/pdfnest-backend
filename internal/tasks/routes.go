package tasks

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

const (
	progressRequesterIDLocalKey   = "task_progress_requester_id"
	progressRequesterTypeLocalKey = "task_progress_requester_type"
	progressCapabilityLocalKey    = "task_progress_capability"
)

func RegisterRoutes(router fiber.Router) {
	router.Get("/v1/tasks/:id", handleGetTaskStatus)
	router.Delete("/v1/tasks/:id", handleCancelTask)
	router.Get("/v1/download/:id", HandleTaskDownload)

	router.Use("/v1/tasks/:id/progress", func(c *fiber.Ctx) error {
		task, err := Registry.Get(c.Params("id"))
		if err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"code": "TASK_STORAGE_UNAVAILABLE"})
		}
		if task == nil {
			return c.SendStatus(fiber.StatusNotFound)
		}
		if !canReadTask(c, task) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"code": "FORBIDDEN"})
		}
		if websocket.IsWebSocketUpgrade(c) {
			requesterID, requesterType := taskRequesterIdentity(c)
			token := strings.TrimSpace(c.Query("token"))
			if token == "" {
				token = strings.TrimSpace(c.Get("X-Download-Token"))
			}
			c.Locals(progressRequesterIDLocalKey, requesterID)
			c.Locals(progressRequesterTypeLocalKey, requesterType)
			c.Locals(progressCapabilityLocalKey, token)
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	router.Get("/v1/tasks/:id/progress", websocket.New(func(c *websocket.Conn) {
		defer c.Close()
		taskId := c.Params("id")
		requesterID, _ := c.Locals(progressRequesterIDLocalKey).(string)
		requesterType, _ := c.Locals(progressRequesterTypeLocalKey).(string)
		token, _ := c.Locals(progressCapabilityLocalKey).(string)
		for {
			progressData, err := Registry.Get(taskId)
			if err != nil || progressData == nil || !isAuthorizedOwnerForIdentity(context.Background(), progressData, requesterID, requesterType, token) {
				break
			}
			err = c.WriteJSON(publicTaskStatus(progressData))
			if err != nil {
				break
			}
			if progressData.Status == "COMPLETED" || progressData.Status == "FAILED" || progressData.Status == "CANCELLED" {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}))
}
