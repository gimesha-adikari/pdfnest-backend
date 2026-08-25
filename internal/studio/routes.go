package studio

import (
	"github.com/gofiber/fiber/v2"
)

// RegisterRoutes mounts all Studio V2 REST API routes onto the router.
func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	studioGroup := router.Group("/studio/v1")

	studioGroup.Post("/sessions", ctrl.CreateSession)
	studioGroup.Get("/sessions/:id", ctrl.GetSession)
	studioGroup.Post("/sessions/:id/operations", ctrl.ApplyOperation)
	studioGroup.Post("/sessions/:id/undo", ctrl.Undo)
	studioGroup.Post("/sessions/:id/redo", ctrl.Redo)
	studioGroup.Get("/sessions/:id/history", ctrl.GetHistory)
	studioGroup.Post("/sessions/:id/checkout", ctrl.Checkout)

	// Phase 3C & 3F: Tile & Preview Endpoints
	studioGroup.Get("/sessions/:id/versions/:version_id/pages/:page_id/tile", ctrl.GetPageTile)
	studioGroup.Get("/metrics", ctrl.GetMetrics)
	studioGroup.Get("/preview/metrics", ctrl.GetMetrics)
}
