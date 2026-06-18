package billing

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

// Simple request architecture mirror layout reflecting core transaction event targets natively
type PaddleWebhookPayload struct {
	EventType string `json:"event_type"`
	Data      struct {
		ID             string `json:"id"`
		CustomerID     string `json:"customer_id"`
		SubscriptionID string `json:"subscription_id"`
		Status         string `json:"status"`
		CustomData     struct {
			UserID string `json:"user_id"`
		} `json:"custom_data"`
	} `json:"data"`
}

func (ctrl *Controller) HandleWebhook(c *fiber.Ctx) error {
	var payload PaddleWebhookPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).SendString("Invalid data schema structural tracking blocks context mapping")
	}

	// Clean security filters handling pass rules logic target updates checks metrics
	if payload.EventType == "subscription.activated" || payload.EventType == "subscription.updated" {
		var sub config.Subscription

		// Look up existing customer mapping metrics trackers values configurations
		err := config.DB.Where("user_id = ?", payload.Data.CustomData.UserID).First(&sub).Error
		if err != nil {
			// Failback structural auto initialization context logic definitions map block
			sub.ID = uuid.New().String()
			sub.UserID = payload.Data.CustomData.UserID
		}

		sub.PaddleCustomerID = payload.Data.CustomerID
		sub.PaddleSubscriptionID = payload.Data.SubscriptionID
		sub.Status = payload.Data.Status
		sub.PlanTier = "pro"
		sub.CurrentPeriodEnd = time.Now().AddDate(0, 1, 0) // Grace block auto configuration increment maps

		config.DB.Save(&sub)
	}

	return c.Status(200).SendString("Webhook received and processed.")
}
