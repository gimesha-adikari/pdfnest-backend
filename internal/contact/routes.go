package contact

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	contactGroup := router.Group("/contact")

	contactGroup.Post(
		"/",
		middleware.OptionalAuth(),
		ctrl.CreateTicket,
	)
}
