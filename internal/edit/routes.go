package edit

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {

	editGroup := router.Group("/edit")

	editGroup.Post("/extract", ctrl.HandleExtractHTML)
	editGroup.Post("/compile", ctrl.HandleCompilePDF)
}
