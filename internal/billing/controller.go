package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Data      struct {
		ID             string  `json:"id"`
		CustomerID     string  `json:"customer_id"`
		SubscriptionID string  `json:"subscription_id"`
		Status         string  `json:"status"`
		Amount         float64 `json:"amount"`
		Currency       string  `json:"currency"`
		CustomData     struct {
			UserID      string `json:"user_id"`
			PackageType string `json:"package_type"`
		} `json:"custom_data"`
		ManagementURLs struct {
			UpdatePaymentMethod string `json:"update_payment_method"`
			Cancel              string `json:"cancel"`
		} `json:"management_urls"`
	} `json:"data"`
}

func (ctrl *Controller) HandleWebhook(c *fiber.Ctx) error {
	rawBody := c.Body()
	signatureHeader := c.Get("Paddle-Signature")

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
		return c.Status(401).SendString("Signature verification failed")
	}

	var payload PaddleWebhookPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).SendString("Invalid webhook data format")
	}

	var existingLog config.WebhookLog
	if err := config.DB.Where("event_id = ?", payload.EventID).First(&existingLog).Error; err == nil {
		return c.Status(200).SendString("Webhook already processed (Idempotent)")
	}

	var sub config.Subscription
	userID := payload.Data.CustomData.UserID

	switch payload.EventType {
	case "subscription.activated", "subscription.updated":
		err := config.DB.Where("user_id = ?", userID).First(&sub).Error
		if err != nil {
			sub.ID = uuid.New().String()
			sub.UserID = userID
		}

		sub.PaddleCustomerID = payload.Data.CustomerID
		sub.PaddleSubscriptionID = payload.Data.SubscriptionID
		sub.Status = payload.Data.Status
		sub.CurrentPeriodEnd = time.Now().AddDate(0, 1, 0)
		sub.UpdateURL = payload.Data.ManagementURLs.UpdatePaymentMethod
		sub.CancelURL = payload.Data.ManagementURLs.Cancel

		if strings.Contains(payload.Data.CustomData.PackageType, "plus") {
			sub.Tier = "plus"
		} else {
			sub.Tier = "pro"
		}
		config.DB.Save(&sub)

	case "subscription.canceled", "subscription.past_due":
		if err := config.DB.Where("paddle_subscription_id = ?", payload.Data.SubscriptionID).First(&sub).Error; err == nil {
			sub.Status = payload.Data.Status
			sub.Tier = "free"
			sub.UpdateURL = ""
			sub.CancelURL = ""
			config.DB.Save(&sub)
		}

	case "transaction.completed":
		if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err == nil {
			pack := payload.Data.CustomData.PackageType
			if strings.Contains(pack, "addon_pack_20") {
				sub.CustomCredits += 20
			} else if strings.Contains(pack, "addon_pack_100") {
				sub.CustomCredits += 100
			} else if strings.Contains(pack, "addon_pack_500") {
				sub.CustomCredits += 500
			} else if strings.Contains(pack, "addon_pack_10") { // Fallbacks
				sub.CustomCredits += 10
			} else if strings.Contains(pack, "addon_pack_50") {
				sub.CustomCredits += 50
			}
			config.DB.Save(&sub)

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

	config.DB.Create(&config.WebhookLog{
		ID:        uuid.New().String(),
		EventID:   payload.EventID,
		EventType: payload.EventType,
		Status:    "processed",
		CreatedAt: time.Now(),
	})

	return c.Status(200).SendString("Webhook processed accurately.")
}

func (ctrl *Controller) GetSubscriptionStatus(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)
	role := c.Locals("role").(string)

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription data not found"})
	}

	return c.JSON(fiber.Map{
		"tier":               sub.Tier,
		"status":             sub.Status,
		"current_period_end": sub.CurrentPeriodEnd,
		"custom_credits":     sub.CustomCredits,
		"update_url":         sub.UpdateURL,
		"cancel_url":         sub.CancelURL,
		"role":               role,
	})
}

func (ctrl *Controller) GetTransactionHistory(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)
	var transactions []config.Transaction
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Find(&transactions)
	return c.JSON(transactions)
}

func (ctrl *Controller) UpgradeMock(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	type UpgradeRequest struct {
		Tier string `json:"tier"`
	}
	var req UpgradeRequest
	if err := c.BodyParser(&req); err != nil || (req.Tier != "plus" && req.Tier != "pro") {
		req.Tier = "plus"
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription row registry not found"})
	}

	sub.Tier = req.Tier
	sub.Status = "active"
	sub.CurrentPeriodEnd = time.Now().AddDate(0, 1, 0) // 1 Month duration mockup
	sub.UpdateURL = "https://sandbox.paddle.com/mock-update"
	sub.CancelURL = "https://sandbox.paddle.com/mock-cancel"

	config.DB.Save(&sub)

	cost := 9.00
	if req.Tier == "pro" {
		cost = 29.00
	}
	tx := config.Transaction{
		ID:                  uuid.New().String(),
		UserID:              userID,
		SubscriptionID:      sub.ID,
		PaddleTransactionID: "MOCK-SUB-" + uuid.New().String()[:8],
		Amount:              cost,
		Currency:            "USD",
		Status:              "completed",
		CreatedAt:           time.Now(),
	}
	config.DB.Create(&tx)

	return c.JSON(fiber.Map{"status": "success", "tier": sub.Tier, "custom_credits": sub.CustomCredits})
}

func (ctrl *Controller) BuyCreditsMock(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)

	type BuyCreditsRequest struct {
		Credits int `json:"credits"`
	}
	var req BuyCreditsRequest
	if err := c.BodyParser(&req); err != nil || req.Credits <= 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid target payload credits definition amount value."})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription record missing."})
	}

	sub.CustomCredits += req.Credits
	config.DB.Save(&sub)

	cost := 5.00
	if req.Credits == 100 {
		cost = 20.00
	} else if req.Credits == 500 {
		cost = 80.00
	}

	tx := config.Transaction{
		ID:                  uuid.New().String(),
		UserID:              userID,
		SubscriptionID:      sub.ID,
		PaddleTransactionID: "MOCK-TX-" + uuid.New().String()[:8],
		Amount:              cost,
		Currency:            "USD",
		Status:              "completed",
		CreatedAt:           time.Now(),
	}
	config.DB.Create(&tx)

	return c.JSON(fiber.Map{
		"status":         "success",
		"custom_credits": sub.CustomCredits,
	})
}
