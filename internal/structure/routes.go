package structure

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {

	router.Post("/structure/analyze", ctrl.Analyze)

	structureGroup := router.Group("/structure", middleware.Protect())

	structureGroup.Post(
		"/merge",
		billing.Use(billing.MergePDF),
		ctrl.Merge,
	)

	structureGroup.Post(
		"/split",
		billing.Use(billing.SplitPDF),
		ctrl.Split,
	)

	structureGroup.Post(
		"/rotate",
		billing.Use(billing.RotatePDF),
		ctrl.Rotate,
	)

	structureGroup.Post(
		"/delete-pages",
		billing.Use(billing.DeletePages),
		ctrl.DeletePages,
	)

	structureGroup.Post(
		"/reorder-pages",
		billing.Use(billing.ReorderPages),
		ctrl.ReorderPages,
	)

	structureGroup.Post(
		"/watermark",
		billing.Use(billing.WatermarkPDF),
		ctrl.Watermark,
	)

	structureGroup.Post(
		"/add-page-numbers",
		billing.Use(billing.AddPageNumbers),
		ctrl.AddPageNumbers,
	)

	structureGroup.Post(
		"/update-metadata",
		billing.Use(billing.UpdateMetadata),
		ctrl.UpdateMetadata,
	)

	structureGroup.Post(
		"/metadata/fetch",
		ctrl.FetchMetadata,
	)

	structureGroup.Post(
		"/repair",
		billing.Use(billing.RepairPDF),
		ctrl.Repair,
	)

	structureGroup.Post(
		"/sign",
		billing.Use(billing.SignPDF),
		ctrl.Sign,
	)

	structureGroup.Post(
		"/crop",
		billing.Use(billing.CropPDF),
		ctrl.Crop,
	)

	structureGroup.Post(
		"/duplicate",
		billing.Use(billing.DuplicatePDF),
		ctrl.Duplicate,
	)

	structureGroup.Post(
		"/insert-blank",
		billing.Use(billing.InsertBlankPDF),
		ctrl.InsertBlank,
	)

	structureGroup.Post(
		"/add-text",
		billing.Use(billing.AddTextPDF),
		ctrl.AddText,
	)

	structureGroup.Post(
		"/highlight",
		billing.Use(billing.HighlightPDF),
		ctrl.Highlight,
	)

	structureGroup.Post(
		"/strikeout",
		billing.Use(billing.StrikeoutPDF),
		ctrl.Strikeout,
	)

	structureGroup.Post(
		"/underline",
		billing.Use(billing.UnderlinePDF),
		ctrl.Underline,
	)
}
