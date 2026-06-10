package structure

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	structureGroup := router.Group("/structure")

	structureGroup.Post("/merge", ctrl.Merge)
	structureGroup.Post("/split", ctrl.Split)
	structureGroup.Post("/rotate", ctrl.Rotate)
	structureGroup.Post("/delete-pages", ctrl.DeletePages)
	structureGroup.Post("/reorder-pages", ctrl.ReorderPages)
	structureGroup.Post("/watermark", ctrl.Watermark)
	structureGroup.Post("/add-page-numbers", ctrl.AddPageNumbers)
	structureGroup.Post("/update-metadata", ctrl.UpdateMetadata)
}
