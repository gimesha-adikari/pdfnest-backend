package security

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	securityGroup := router.Group("/security", middleware.Protect(), middleware.EnforceLimits())

	securityGroup.Post("/lock", ctrl.Lock)
	securityGroup.Post("/unlock", ctrl.Unlock)
	securityGroup.Post("/redact-text", ctrl.HandleRedaction)
}
