package storage

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	group := router.Group("/storage", middleware.Protect())
	group.Post("/r2/presign", ctrl.PresignUploads)
}
