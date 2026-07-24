package user

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	userGroup := router.Group("/user", middleware.Protect())

	userGroup.Put("/settings/password", ctrl.UpdatePassword)

	userGroup.Get("/settings/preferences", ctrl.GetPreferences)
	userGroup.Put("/settings/preferences", ctrl.UpdatePreferences)

	userGroup.Get("/settings/export", ctrl.ExportData)

	userGroup.Delete("/account", ctrl.DeleteAccount)
}
