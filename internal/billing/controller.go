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

type billingLimits struct {
	Units3H    int
	UnitsDay   int
	UnitsMonth int
}

func (ctrl *Controller) HandleWebhook(c *fiber.Ctx) error {
	rawBody := c.Body()
	signatureHeader := c.Get("Paddle-Signature")
	if strings.TrimSpace(signatureHeader) == "" {
		return c.Status(401).SendString("Missing signature header")
	}

	parts := strings.Split(signatureHeader, ";")
	if len(parts) != 2 {
		return c.Status(401).SendString("Invalid signature format")
	}

	tsPart := strings.TrimPrefix(strings.TrimSpace(parts[0]), "ts=")
	h1Part := strings.TrimPrefix(strings.TrimSpace(parts[1]), "h1=")
	if tsPart == "" || h1Part == "" {
		return c.Status(401).SendString("Invalid signature format")
	}

	signedPayload := tsPart + ":" + string(rawBody)

	secretKey := os.Getenv("PADDLE_WEBHOOK_SECRET")
	if secretKey == "" {
		return c.Status(500).SendString("Webhook secret not configured")
	}

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

	if strings.TrimSpace(payload.EventID) == "" {
		return c.Status(400).SendString("Missing webhook event_id")
	}

	var existingLog config.WebhookLog
	if err := config.DB.Where("event_id = ?", payload.EventID).First(&existingLog).Error; err == nil {
		return c.Status(200).SendString("Webhook already processed (Idempotent)")
	}

	now := time.Now()
	userID := payload.Data.CustomData.UserID

	var sub config.Subscription

	switch payload.EventType {
	case "subscription.activated", "subscription.updated":
		err := config.DB.Where("user_id = ?", userID).First(&sub).Error
		if err != nil {
			sub.ID = uuid.New().String()
			sub.UserID = userID
			sub.CreatedAt = now
		}

		sub.PaddleCustomerID = payload.Data.CustomerID
		sub.PaddleSubscriptionID = payload.Data.SubscriptionID
		sub.Status = payload.Data.Status
		sub.CurrentPeriodEnd = now.AddDate(0, 1, 0)
		sub.UpdateURL = payload.Data.ManagementURLs.UpdatePaymentMethod
		sub.CancelURL = payload.Data.ManagementURLs.Cancel

		// Keep the current tier mapping: plus package -> plus, otherwise pro.
		if strings.Contains(strings.ToLower(payload.Data.CustomData.PackageType), "plus") {
			sub.Tier = "plus"
		} else {
			sub.Tier = "pro"
		}

		// Fresh billing cycle starts on activation / renewal.
		resetBillingWindows(&sub, now)
		sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
		sub.UsedUnitsMonthly = 0
		sub.CustomCredits = maxInt(sub.CustomCredits, 0)
		sub.UpdatedAt = now

		if err := config.DB.Save(&sub).Error; err != nil {
			return c.Status(500).SendString("Failed to save subscription state")
		}

	case "subscription.canceled", "subscription.past_due":
		if err := config.DB.Where("paddle_subscription_id = ?", payload.Data.SubscriptionID).First(&sub).Error; err == nil {
			sub.Status = payload.Data.Status
			sub.Tier = "free"
			sub.UpdateURL = ""
			sub.CancelURL = ""
			sub.UpdatedAt = now
			if err := config.DB.Save(&sub).Error; err != nil {
				return c.Status(500).SendString("Failed to save cancellation state")
			}
		}

	case "transaction.completed":
		if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err == nil {
			packUnits := packageUnits(payload.Data.CustomData.PackageType)
			if packUnits > 0 {
				sub.CustomCredits += packUnits
				sub.UpdatedAt = now
				if err := config.DB.Save(&sub).Error; err != nil {
					return c.Status(500).SendString("Failed to add credit units")
				}
			}

			tx := config.Transaction{
				ID:                  uuid.New().String(),
				UserID:              sub.UserID,
				SubscriptionID:      sub.ID,
				PaddleTransactionID: payload.Data.ID,
				Amount:              payload.Data.Amount,
				Currency:            payload.Data.Currency,
				Status:              "completed",
				CreatedAt:           now,
			}
			if err := config.DB.Create(&tx).Error; err != nil {
				return c.Status(500).SendString("Failed to record transaction")
			}
		}
	}

	if err := config.DB.Create(&config.WebhookLog{
		ID:        uuid.New().String(),
		EventID:   payload.EventID,
		EventType: payload.EventType,
		Status:    "processed",
		CreatedAt: now,
	}).Error; err != nil {
		return c.Status(500).SendString("Failed to record webhook log")
	}

	return c.Status(200).SendString("Webhook processed accurately.")
}

func (ctrl *Controller) GetSubscriptionStatus(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	role, _ := c.Locals("role").(string)

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription data not found"})
	}

	limits := limitsForTier(sub.Tier)
	resetBillingWindows(&sub, time.Now())

	monthlyRemaining := limits.UnitsMonth + sub.CustomCredits - sub.UsedUnitsMonthly
	if monthlyRemaining < 0 {
		monthlyRemaining = 0
	}
	dailyRemaining := limits.UnitsDay - sub.UsedUnitsDaily
	if dailyRemaining < 0 {
		dailyRemaining = 0
	}
	threeHourRemaining := limits.Units3H - sub.UsedUnits3h
	if threeHourRemaining < 0 {
		threeHourRemaining = 0
	}

	return c.JSON(fiber.Map{
		"tier":                  sub.Tier,
		"status":                sub.Status,
		"current_period_end":    sub.CurrentPeriodEnd,
		"custom_credits":        sub.CustomCredits,
		"update_url":            sub.UpdateURL,
		"cancel_url":            sub.CancelURL,
		"role":                  role,
		"used_units_3h":         sub.UsedUnits3h,
		"used_units_daily":      sub.UsedUnitsDaily,
		"used_units_monthly":    sub.UsedUnitsMonthly,
		"three_hour_limit":      limits.Units3H,
		"daily_limit":           limits.UnitsDay,
		"monthly_limit":         limits.UnitsMonth + sub.CustomCredits,
		"three_hour_remaining":  threeHourRemaining,
		"daily_remaining":       dailyRemaining,
		"monthly_remaining":     monthlyRemaining,
		"window_3h_reset_at":    sub.Window3HResetAt,
		"window_daily_reset_at": sub.WindowDailyResetAt,
		"window_month_reset_at": sub.WindowMonthlyResetAt,
	})
}

func (ctrl *Controller) GetTransactionHistory(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	var transactions []config.Transaction
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Find(&transactions)
	return c.JSON(transactions)
}

func (ctrl *Controller) UpgradeMock(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

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

	now := time.Now()

	sub.Tier = req.Tier
	sub.Status = "active"
	sub.CurrentPeriodEnd = now.AddDate(0, 1, 0)
	sub.UpdateURL = "https://sandbox.paddle.com/mock-update"
	sub.CancelURL = "https://sandbox.paddle.com/mock-cancel"

	// Fresh cycle on upgrade.
	resetBillingWindows(&sub, now)
	sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
	sub.UsedUnitsMonthly = 0

	sub.UpdatedAt = now

	if err := config.DB.Save(&sub).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to save upgraded subscription"})
	}

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
		CreatedAt:           now,
	}
	if err := config.DB.Create(&tx).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to record upgrade transaction"})
	}

	return c.JSON(fiber.Map{
		"status":         "success",
		"tier":           sub.Tier,
		"custom_credits": sub.CustomCredits,
	})
}

func (ctrl *Controller) BuyCreditsMock(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

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

	now := time.Now()

	sub.CustomCredits += req.Credits
	sub.UpdatedAt = now

	if err := config.DB.Save(&sub).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to add credits"})
	}

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
		CreatedAt:           now,
	}
	if err := config.DB.Create(&tx).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to record credit transaction"})
	}

	return c.JSON(fiber.Map{
		"status":         "success",
		"custom_credits": sub.CustomCredits,
	})
}

func resetBillingWindows(sub *config.Subscription, now time.Time) {
	if sub.Window3HResetAt.IsZero() || !now.Before(sub.Window3HResetAt) {
		sub.UsedUnits3h = 0
		sub.Window3HResetAt = now.Truncate(3 * time.Hour).Add(3 * time.Hour)
	}

	if sub.WindowDailyResetAt.IsZero() || !now.Before(sub.WindowDailyResetAt) {
		sub.UsedUnitsDaily = 0
		sub.WindowDailyResetAt = nextMidnight(now)
	}

	if sub.WindowMonthlyResetAt.IsZero() || !now.Before(sub.WindowMonthlyResetAt) {
		sub.UsedUnitsMonthly = 0
		sub.WindowMonthlyResetAt = nextMonthStart(now)
	}
}

func limitsForTier(tier string) billingLimits {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "pro":
		return billingLimits{Units3H: 80, UnitsDay: 250, UnitsMonth: 1000}
	case "plus":
		return billingLimits{Units3H: 20, UnitsDay: 60, UnitsMonth: 250}
	default:
		return billingLimits{Units3H: 8, UnitsDay: 20, UnitsMonth: 80}
	}
}

func packageUnits(packageType string) int {
	pack := strings.ToLower(strings.TrimSpace(packageType))

	switch {
	case strings.Contains(pack, "addon_pack_500"):
		return 500
	case strings.Contains(pack, "addon_pack_100"):
		return 100
	case strings.Contains(pack, "addon_pack_50"):
		return 50
	case strings.Contains(pack, "addon_pack_20"):
		return 20
	case strings.Contains(pack, "addon_pack_10"):
		return 10
	default:
		return 0
	}
}

func nextMidnight(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

func nextMonthStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
