package conversion

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	conversionGroup := router.Group("/conversion")

	conversionGroup.Post("/to-pdf", ctrl.ConvertImagesToPDF)
	conversionGroup.Post("/pdf-to-images", ctrl.RasterizePdfUniversal)
	conversionGroup.Post("/preview/page", ctrl.StreamPagePreviewHandler)
	conversionGroup.Post("/word-to-pdf", ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/excel-to-pdf", ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/powerpoint-to-pdf", ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/url-to-pdf", ctrl.ConvertUrlToPDF)
	conversionGroup.Post("/markdown-to-pdf", ctrl.ConvertMarkdownToPDF)
	conversionGroup.Post("/code-to-pdf", ctrl.ConvertCodeToPDF)
	conversionGroup.Post("/html-to-pdf-async", ctrl.HandleAsyncHTMLToPDF)
	conversionGroup.Post("/markdown-to-pdf-async", ctrl.HandleAsyncMarkdownToPDF)
	conversionGroup.Post("/pdf-to-word", ConvertPdfToOfficeHandler("docx"))
	conversionGroup.Post("/pdf-to-excel", ConvertPdfToOfficeHandler("xlsx"))
	conversionGroup.Post("/pdf-to-powerpoint", ConvertPdfToOfficeHandler("pptx"))
}
