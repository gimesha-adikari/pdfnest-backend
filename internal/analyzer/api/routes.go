package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

// RegisterRoutes registers all analyzer endpoints on the provided Fiber router group.
func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	group := router.Group("/v1/analyzer")

	// Session Lifecycle
	group.Post("/sessions", ctrl.CreateSession)
	group.Get("/sessions/:id", ctrl.GetSession)
	group.Get("/sessions/:id/tree", ctrl.GetTree)
	group.Put("/sessions/:id/scope", ctrl.UpdateScope)
	group.Post("/sessions/:id/analyze", ctrl.Analyze)
	group.Get("/sessions/:id/result", ctrl.GetResult)

	// Readiness & Health Probe
	group.Get("/readiness", ctrl.GetReadiness)

	// Task Progress & Status
	group.Get("/tasks/:id", ctrl.GetTaskStatus)

	// WebSocket Upgrade & Connection
	group.Use("/tasks/:id/progress", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	group.Get("/tasks/:id/progress", websocket.New(ctrl.HandleWebSocketProgress))
}
