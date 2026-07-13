package security

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {

	securityGroup := router.Group("/security", middleware.Protect())

	securityGroup.Post(
		"/lock",
		billing.Use(billing.LockPDF),
		ctrl.Lock,
	)

	securityGroup.Post(
		"/unlock",
		billing.Use(billing.UnlockPDF),
		ctrl.Unlock,
	)

	securityGroup.Post(
		"/redact-text",
		billing.Use(billing.RedactTextPDF),
		ctrl.HandleRedaction,
	)
}
