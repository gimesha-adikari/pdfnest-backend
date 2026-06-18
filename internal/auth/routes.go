package auth

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	authGroup := router.Group("/auth")
	authGroup.Post("/register", ctrl.Register)
	authGroup.Post("/login", ctrl.Login)
	authGroup.Post("/google", ctrl.GoogleSignIn)
}
