package ocr

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	ocrGroup := router.Group("/ocr", middleware.Protect(), middleware.EnforceLimits())

	ocrGroup.Post("/extract-text", ctrl.ProcessOCR)
	ocrGroup.Post("/to-text-pdf", ctrl.ProcessImageToTextPDF)
	ocrGroup.Post("/extract-text-async", ctrl.HandleAsyncExtractText)
	ocrGroup.Post("/to-text-pdf-async", ctrl.HandleAsyncImageToTextPDF)
}
