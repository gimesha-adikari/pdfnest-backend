package markup

import (
	"pdfnest-backend/internal/billing"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	structureGroup := router.Group("/markup")

	structureGroup.Post("/highlight", billing.Use(billing.HighlightPDF), ctrl.HandleHighlight)
	structureGroup.Post("/underline", billing.Use(billing.UnderlinePDF), ctrl.HandleUnderline)
	structureGroup.Post("/strikeout", billing.Use(billing.StrikeoutPDF), ctrl.HandleStrikeout)

	structureGroup.Get("/jobs/:job_id", ctrl.HandleJobStatus)
	structureGroup.Get("/jobs/:job_id/download", ctrl.HandleJobDownload)
}
