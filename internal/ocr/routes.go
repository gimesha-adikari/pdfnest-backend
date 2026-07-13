// file: internal/ocr/routes.go
package ocr

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	ocrGroup := router.Group("/ocr", middleware.Protect())

	ocrGroup.Post("/extract-text", billing.Use(billing.ExtractTextPDF), ctrl.ProcessOCR)
	ocrGroup.Post("/to-text-pdf", billing.Use(billing.ImageToTextPDF), ctrl.ProcessImageToTextPDF)

	ocrGroup.Post("/extract-text-async", ctrl.HandleAsyncExtractText)
	ocrGroup.Post("/to-text-pdf-async", ctrl.HandleAsyncImageToTextPDF)
}
