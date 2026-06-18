package optimize

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	optimizeGroup := router.Group("/optimize", middleware.Protect(), middleware.EnforceLimits())

	optimizeGroup.Post("/compress", ctrl.Compress)
	optimizeGroup.Post("/grayscale", ctrl.Grayscale)
}
