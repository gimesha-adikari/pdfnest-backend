package billing

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	// NOTE: This explicitly avoids auth guards because Paddle servers trigger it down open lines securely
	router.Post("/billing/webhook", ctrl.HandleWebhook)
}
