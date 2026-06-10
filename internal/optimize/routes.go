package optimize

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	optimizeGroup := router.Group("/optimize")

	optimizeGroup.Post("/compress", ctrl.Compress)
}
