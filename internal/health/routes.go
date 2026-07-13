package health

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, controller *Controller) {
	router.Get("/health", controller.Health)
}
