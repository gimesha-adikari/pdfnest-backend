package auth

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	authGroup := router.Group("/auth")
	authGroup.Post("/register", ctrl.Register)
	authGroup.Post("/login", ctrl.Login)
	authGroup.Post("/google", ctrl.GoogleSignIn)
	authGroup.Post("/logout", ctrl.Logout)

	authGroup.Get("/verify-email", ctrl.VerifyEmail)
	authGroup.Post("/verify-email", ctrl.VerifyEmail)
	authGroup.Post("/resend-verification", ctrl.ResendVerification)
}
