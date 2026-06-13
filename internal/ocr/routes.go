package ocr

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	ocrGroup := router.Group("/ocr")

	ocrGroup.Post("/extract-text", ctrl.ProcessOCR)
	ocrGroup.Post("/to-text-pdf", ctrl.ConvertImagesToTextPDF)
}
