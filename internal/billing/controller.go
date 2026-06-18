package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"pdfnest-backend/config"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

type PaddleWebhookPayload struct {
	EventType string `json:"event_type"`
	Data      struct {
		ID             string  `json:"id"`
		CustomerID     string  `json:"customer_id"`
		SubscriptionID string  `json:"subscription_id"`
		Status         string  `json:"status"`
		Amount         float64 `json:"amount"`   // For transactions
		Currency       string  `json:"currency"` // For transactions
		CustomData     struct {
			UserID string `json:"user_id"`
		} `json:"custom_data"`
	} `json:"data"`
}

func (ctrl *Controller) HandleWebhook(c *fiber.Ctx) error {

	rawBody := c.Body()
	signatureHeader := c.Get("Paddle-Signature") // Format: ts=123456789;h1=abcd...

	parts := strings.Split(signatureHeader, ";")
	if len(parts) != 2 {
		return c.Status(401).SendString("Invalid signature format")
	}

	tsPart := strings.TrimPrefix(parts[0], "ts=")
	h1Part := strings.TrimPrefix(parts[1], "h1=")

	signedPayload := tsPart + ":" + string(rawBody)

	secretKey := os.Getenv("PADDLE_WEBHOOK_SECRET")
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(signedPayload))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(h1Part), []byte(expectedHash)) {
		log.Println("WARNING: Blocked forged billing webhook attempt!")
		return c.Status(401).SendString("Signature verification failed")
	}

	var payload PaddleWebhookPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).SendString("Invalid webhook data format")
	}

	var sub config.Subscription

	switch payload.EventType {
	case "subscription.activated", "subscription.updated":
		err := config.DB.Where("user_id = ?", payload.Data.CustomData.UserID).First(&sub).Error
		if err != nil {
			sub.ID = uuid.New().String()
			sub.UserID = payload.Data.CustomData.UserID
		}

		sub.PaddleCustomerID = payload.Data.CustomerID
		sub.PaddleSubscriptionID = payload.Data.SubscriptionID
		sub.Status = payload.Data.Status
		sub.Tier = "pro"
		sub.CurrentPeriodEnd = time.Now().AddDate(0, 1, 0) // Adds 1 month

		config.DB.Save(&sub)

	case "subscription.canceled", "subscription.past_due":
		if err := config.DB.Where("paddle_subscription_id = ?", payload.Data.SubscriptionID).First(&sub).Error; err == nil {
			sub.Status = payload.Data.Status
			sub.Tier = "free"
			config.DB.Save(&sub)
		}

	case "transaction.completed":
		if err := config.DB.Where("paddle_subscription_id = ?", payload.Data.SubscriptionID).First(&sub).Error; err == nil {
			tx := config.Transaction{
				ID:                  uuid.New().String(),
				UserID:              sub.UserID,
				SubscriptionID:      sub.ID,
				PaddleTransactionID: payload.Data.ID,
				Amount:              payload.Data.Amount,
				Currency:            payload.Data.Currency,
				Status:              "completed",
				CreatedAt:           time.Now(),
			}
			config.DB.Create(&tx)
		}
	}

	return c.Status(200).SendString("Webhook received and processed.")
}

func (ctrl *Controller) GetSubscriptionStatus(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string) // Requires Auth Middleware

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription data not found"})
	}

	return c.JSON(fiber.Map{
		"tier":               sub.Tier,
		"status":             sub.Status,
		"current_period_end": sub.CurrentPeriodEnd,
	})
}

func (ctrl *Controller) GetTransactionHistory(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	var transactions []config.Transaction
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Find(&transactions)

	return c.JSON(transactions)
}
