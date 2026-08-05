package ocr

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	ocrGroup := router.Group("/ocr")

	ocrGroup.Get("/languages", ctrl.Languages)
	ocrGroup.Post("/jobs", ctrl.HandleAsyncImageToTextPDFR2)

	uploadGroup := ocrGroup.Group("", uploads.Prepare())

	uploadGroup.Post("/extract-text", billing.Use(billing.ExtractTextPDF), ctrl.ProcessOCR)
	uploadGroup.Post("/to-text-pdf", billing.Use(billing.ImageToTextPDF), ctrl.ProcessImageToTextPDF)

	uploadGroup.Post("/extract-text-async", ctrl.HandleAsyncExtractText)
	uploadGroup.Post("/to-text-pdf-async", ctrl.HandleAsyncImageToTextPDF)
}
