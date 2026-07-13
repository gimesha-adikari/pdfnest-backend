package optimize

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {

	optimizeGroup := router.Group("/optimize", middleware.Protect())

	optimizeGroup.Post(
		"/compress",
		billing.Use(billing.CompressPDF),
		ctrl.Compress,
	)

	optimizeGroup.Post(
		"/grayscale",
		billing.Use(billing.GrayscalePDF),
		ctrl.Grayscale,
	)
}
