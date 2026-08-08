package tasks

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

func RegisterRoutes(router fiber.Router) {
	router.Get("/v1/tasks/:id", handleGetTaskStatus)
	router.Delete("/v1/tasks/:id", handleCancelTask)
	router.Get("/v1/download/:id", HandleTaskDownload)

	router.Use("/v1/tasks/:id/progress", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	router.Get("/v1/tasks/:id/progress", websocket.New(func(c *websocket.Conn) {
		taskId := c.Params("id")
		for {
			progressData := getTaskProgress(taskId)
			err := c.WriteJSON(progressData)
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
