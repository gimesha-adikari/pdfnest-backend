package admin

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	adminGroup := router.Group("/admin", middleware.Protect(), middleware.RequireAdmin())

	adminGroup.Get("/users", ctrl.ListUsers)
	adminGroup.Get("/users/:id/details", ctrl.GetUserDetails)
	adminGroup.Get("/metrics", ctrl.GetDashboardMetrics)
	adminGroup.Patch("/users/:id/ban", ctrl.ToggleBanUser)
	adminGroup.Patch("/users/:id/role", ctrl.UpdateUserRole)
	adminGroup.Get("/subscriptions", ctrl.ListSubscriptions)
	adminGroup.Patch("/users/:id/tier", ctrl.UpdateUserTier)
}
