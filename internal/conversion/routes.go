package conversion

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	router.Post("/conversion/preview/page", ctrl.StreamPagePreviewHandler)

	conversionGroup := router.Group("/conversion", middleware.Protect())

	conversionGroup.Post("/to-pdf", billing.Use(billing.ConvertImagesToPDF), ctrl.ConvertImagesToPDF)
	conversionGroup.Post("/custom-to-pdf", billing.Use(billing.ConvertCustomImagesToPDF), ctrl.ConvertCustomImagesToPDF)
	conversionGroup.Post("/pdf-to-images", billing.Use(billing.RasterizePDFUniversal), ctrl.RasterizePdfUniversal)

	conversionGroup.Post("/word-to-pdf", billing.Use(billing.ConvertOfficeToPDFWord), ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/excel-to-pdf", billing.Use(billing.ConvertOfficeToPDFExcel), ctrl.ConvertOfficeToPDF)
	conversionGroup.Post("/powerpoint-to-pdf", billing.Use(billing.ConvertOfficeToPDFPowerPoint), ctrl.ConvertOfficeToPDF)

	conversionGroup.Post("/url-to-pdf", billing.Use(billing.ConvertURLToPDF), ctrl.ConvertUrlToPDF)
	conversionGroup.Post("/markdown-to-pdf", billing.Use(billing.ConvertMarkdownToPDF), ctrl.ConvertMarkdownToPDF)
	conversionGroup.Post("/code-to-pdf", billing.Use(billing.ConvertCodeToPDF), ctrl.ConvertCodeToPDF)

	conversionGroup.Post("/html-to-pdf-async", ctrl.HandleAsyncHTMLToPDF)
	conversionGroup.Post("/markdown-to-pdf-async", ctrl.HandleAsyncMarkdownToPDF)

	conversionGroup.Post("/pdf-to-word", billing.Use(billing.ConvertPDFToWord), ConvertPdfToOfficeHandler("docx"))
	conversionGroup.Post("/pdf-to-excel", billing.Use(billing.ConvertPDFToExcel), ConvertPdfToOfficeHandler("xlsx"))
	conversionGroup.Post("/pdf-to-powerpoint", billing.Use(billing.ConvertPDFToPowerPoint), ConvertPdfToOfficeHandler("pptx"))
}
