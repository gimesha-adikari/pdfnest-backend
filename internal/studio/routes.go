package studio

import (
	"github.com/gofiber/fiber/v2"

	"pdfnest-backend/internal/uploads"
)

// RegisterRoutes mounts all Studio V2 REST API routes onto the router.
func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	studioGroup := router.Group("/studio/v1")

	studioGroup.Post("/sessions", ctrl.CreateSession)
	studioGroup.Post("/sessions/from-upload", uploads.Prepare(), ctrl.CreateSessionFromUpload)
	studioGroup.Get("/sessions/:id", ctrl.GetSession)
	studioGroup.Post("/sessions/:id/assets", uploads.Prepare(), ctrl.CreateSecondaryAsset)
	studioGroup.Post("/sessions/:id/operations", ctrl.ApplyOperation)
	studioGroup.Post("/sessions/:id/commands", ctrl.ExecuteCommand)
	studioGroup.Post("/sessions/:id/undo", ctrl.Undo)
	studioGroup.Post("/sessions/:id/redo", ctrl.Redo)
	studioGroup.Get("/sessions/:id/history", ctrl.GetHistory)
	studioGroup.Post("/sessions/:id/checkout", ctrl.Checkout)
	studioGroup.Post("/sessions/:id/materializations", ctrl.Materialize)
	studioGroup.Post("/sessions/:id/export", ctrl.FinalizeExport)
	studioGroup.Get("/sessions/:id/exports/:export_id/download", ctrl.DownloadExport)

	// Phase 3C & 3F: Tile & Preview Endpoints
	studioGroup.Get("/sessions/:id/versions/:version_id/pages/:page_id/tile", ctrl.GetPageTile)
	studioGroup.Get("/metrics", ctrl.GetMetrics)
	studioGroup.Get("/preview/metrics", ctrl.GetMetrics)
}
