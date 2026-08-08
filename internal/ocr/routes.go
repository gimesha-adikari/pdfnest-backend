package ocr

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	ocrGroup := router.Group("/ocr")

	ocrGroup.Get("/languages", ctrl.Languages)
	ocrGroup.Post("/jobs", ctrl.HandleAsyncImageToTextPDFR2)

	uploadGroup := ocrGroup.Group("", uploads.Prepare(), limiter.Default.Middleware())

	uploadGroup.Post("/extract-text", billing.Use(billing.ExtractTextPDF), ctrl.ProcessOCR)
	uploadGroup.Post("/to-text-pdf", billing.Use(billing.ImageToTextPDF), ctrl.ProcessImageToTextPDF)

	uploadGroup.Post("/extract-text-async", idempotency.Use(nil), ctrl.HandleAsyncExtractText)
	uploadGroup.Post("/to-text-pdf-async", idempotency.Use(nil), ctrl.HandleAsyncImageToTextPDF)
}
