package edit

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	editGroup := router.Group("/edit", middleware.Protect())
	editGroup.Post("/extract", billing.Use(billing.PDFEditExtract), ctrl.HandleExtractHTML)
	editGroup.Post("/compile", billing.Use(billing.PDFEditCompile), ctrl.HandleCompilePDF)

	editGroup.Get("/jobs/:job_id", ctrl.HandleJobStatus)
	editGroup.Get("/jobs/:job_id/download", ctrl.HandleJobDownload)
}
