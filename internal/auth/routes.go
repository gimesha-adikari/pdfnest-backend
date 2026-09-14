package auth

import (
	"pdfnest-backend/internal/identity"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller, identityStore *identity.Store) {
	authGroup := router.Group("/auth")
	authGroup.Post("/register", ctrl.Register)
	authGroup.Post("/login", ctrl.Login)
	authGroup.Post("/google", ctrl.GoogleSignIn)
	authGroup.Post("/logout", ctrl.Logout)
	authGroup.Post("/request-password-reset", ctrl.RequestPasswordReset)
	// Keep the concise route available for clients that use the page name while
	// sharing the same handler and token contract.
	authGroup.Post("/forgot-password", ctrl.RequestPasswordReset)
	authGroup.Post("/reset-password", ctrl.ResetPassword)

	authGroup.Get("/session", identity.Resolve(identityStore), ctrl.Session)

	authGroup.Get("/verify-email", ctrl.VerifyEmail)
	authGroup.Post("/verify-email", ctrl.VerifyEmail)
	authGroup.Post("/resend-verification", ctrl.ResendVerification)
}
