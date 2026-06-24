package content

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	toolCtrl := NewToolController()

	router.Get("/site-content/home", ctrl.GetHomePageContent)
	router.Get("/site-content/subscribe", ctrl.GetSubscribePageContent)
	router.Get("/site-content/about", ctrl.GetAboutPageContent)
	router.Get("/site-content/tools", toolCtrl.GetPublicTools)

	admin := router.Group(
		"/admin/site-content",
		middleware.Protect(),
		middleware.RequireAdmin(),
	)

	admin.Put("/home", ctrl.UpdateHomePageContent)
	admin.Put("/subscribe", ctrl.UpdateSubscribePageContent)
	admin.Put("/about", ctrl.UpdateAboutPageContent)
	admin.Put("/tools-config", toolCtrl.UpdateToolConfiguration)
}
