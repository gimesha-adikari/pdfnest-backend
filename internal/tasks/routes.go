package tasks

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

func RegisterRoutes(app *fiber.App) {
	app.Get("/api/v1/tasks/:id", handleGetTaskStatus)
	app.Get("/api/v1/download/:id", HandleTaskDownload)

	app.Use("/api/v1/tasks/:id/progress", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	app.Get("/api/v1/tasks/:id/progress", websocket.New(func(c *websocket.Conn) {
		taskId := c.Params("id")
		for {
			progressData := getTaskProgress(taskId)
			err := c.WriteJSON(progressData)
			if err != nil {
				break
			}
			if progressData.Status == "COMPLETED" || progressData.Status == "FAILED" {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}))
}
