package conversion

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	conversionGroup := router.Group("/conversion")
	conversionGroup.Post("/to-pdf", ctrl.ConvertImagesToPDF)
	conversionGroup.Post("/pdf-to-images", ctrl.RasterizePdfUniversal)

	apiConversionGroup := router.Group("/api/conversion")
	apiConversionGroup.Post("/pdf-to-images", ctrl.RasterizePdfUniversal)
}
