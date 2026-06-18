package edit

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {

	editGroup := router.Group("/edit", middleware.Protect(), middleware.EnforceLimits())

	editGroup.Post("/extract", ctrl.HandleExtractHTML)
	editGroup.Post("/compile", ctrl.HandleCompilePDF)
}
