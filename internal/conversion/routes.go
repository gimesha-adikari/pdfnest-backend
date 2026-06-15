package conversion

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	conversionGroup := router.Group("/conversion")

	conversionGroup.Post("/to-pdf", ctrl.ConvertImagesToPDF)
	conversionGroup.Post("/pdf-to-images", ctrl.RasterizePdfUniversal)
	conversionGroup.Post("/preview/page", ctrl.StreamPagePreviewHandler)
	conversionGroup.Post("/office-to-pdf", ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/url-to-pdf", ctrl.ConvertUrlToPDF)
	conversionGroup.Post("/markdown-to-pdf", ctrl.ConvertMarkdownToPDF)
	conversionGroup.Post("/code-to-pdf", ctrl.ConvertCodeToPDF)
	router.Post("/conversion/html-to-pdf-async", ctrl.HandleAsyncHTMLToPDF)
	router.Post("/conversion/markdown-to-pdf-async", ctrl.HandleAsyncMarkdownToPDF)

}
