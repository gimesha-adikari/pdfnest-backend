package billing

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	router.Post("/billing/webhook", ctrl.HandleWebhook)

	billingGroup := router.Group("/billing", middleware.Protect()) // Assuming Protect() verifies JWT
	billingGroup.Get("/status", ctrl.GetSubscriptionStatus)
	billingGroup.Get("/transactions", ctrl.GetTransactionHistory)
}
