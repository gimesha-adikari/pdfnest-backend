package contact

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterAdminRoutes(router fiber.Router, ctrl *AdminController) {
	admin := router.Group("/admin/contact")
	admin.Use(middleware.Protect(), middleware.RequireAdmin())

	admin.Get("/dashboard", ctrl.Dashboard)
	admin.Get("/tickets", ctrl.ListTickets)
	admin.Get("/tickets/:id", ctrl.GetTicket)
	admin.Patch("/tickets/:id/status", ctrl.UpdateStatus)
	admin.Patch("/tickets/:id/priority", ctrl.UpdatePriority)
	admin.Patch("/tickets/:id/notes", ctrl.UpdateNotes)
	admin.Post("/tickets/:id/reply", ctrl.ReplyTicket)

	admin.Get("/categories", ctrl.ListCategories)
	admin.Post("/categories", ctrl.CreateCategory)
	admin.Patch("/categories/:id", ctrl.UpdateCategory)
	admin.Delete("/categories/:id", ctrl.DeleteCategory)

	admin.Get("/reports", ctrl.Reports)
}
