package structure

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	structureGroup := router.Group("/structure", middleware.Protect(), middleware.EnforceLimits())

	structureGroup.Post("/merge", ctrl.Merge)
	structureGroup.Post("/split", ctrl.Split)
	structureGroup.Post("/rotate", ctrl.Rotate)
	structureGroup.Post("/delete-pages", ctrl.DeletePages)
	structureGroup.Post("/reorder-pages", ctrl.ReorderPages)
	structureGroup.Post("/watermark", ctrl.Watermark)
	structureGroup.Post("/add-page-numbers", ctrl.AddPageNumbers)
	structureGroup.Post("/update-metadata", ctrl.UpdateMetadata)
	structureGroup.Post("/metadata/fetch", ctrl.FetchMetadata)
	structureGroup.Post("/repair", ctrl.Repair)
	structureGroup.Post("/sign", ctrl.Sign)
	structureGroup.Post("/crop", ctrl.Crop)
}
