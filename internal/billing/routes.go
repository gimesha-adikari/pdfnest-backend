package billing

import (
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, ctrl *Controller) {
	router.Post("/billing/webhook", ctrl.HandleWebhook)

	billingGroup := router.Group("/billing", middleware.Protect())
	billingGroup.Get("/status", ctrl.GetSubscriptionStatus)
	billingGroup.Get("/transactions", ctrl.GetTransactionHistory)

	billingGroup.Post("/checkout", ctrl.CreateCheckout)
	billingGroup.Post("/checkout-credits", ctrl.CreateCreditCheckout)

	billingGroup.Post("/upgrade-mock", ctrl.UpgradeMock)
	billingGroup.Post("/buy-credits-mock", ctrl.BuyCreditsMock)
}
