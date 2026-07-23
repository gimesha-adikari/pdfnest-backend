package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"pdfnest-backend/config"
	"strconv"
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
		ID             string `json:"id"`
		CustomerID     string `json:"customer_id"`
		SubscriptionID string `json:"subscription_id"`
		Status         string `json:"status"`

		CurrencyCode string `json:"currency_code"`

		CustomData struct {
			UserID          string `json:"user_id"`
			PackageType     string `json:"package_type"`
			BillingInterval string `json:"billing_interval"`
			PurchaseType    string `json:"purchase_type"`
		} `json:"custom_data"`

		BillingCycle struct {
			Interval  string `json:"interval"`
			Frequency int    `json:"frequency"`
		} `json:"billing_cycle"`

		TrialDates struct {
			StartsAt time.Time `json:"starts_at"`
			EndsAt   time.Time `json:"ends_at"`
		} `json:"trial_dates"`

		NextBilledAt time.Time `json:"next_billed_at"`

		CurrentBillingPeriod struct {
			StartsAt time.Time `json:"starts_at"`
			EndsAt   time.Time `json:"ends_at"`
		} `json:"current_billing_period"`

		BillingPeriod struct {
			StartsAt time.Time `json:"starts_at"`
			EndsAt   time.Time `json:"ends_at"`
		} `json:"billing_period"`

		Details struct {
			Totals struct {
				GrandTotal   string `json:"grand_total"`
				Total        string `json:"total"`
				CurrencyCode string `json:"currency_code"`
			} `json:"totals"`
		} `json:"details"`

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

	log.Println("========================================")
	log.Println("[PADDLE WEBHOOK] Incoming webhook")
	log.Println("[PADDLE WEBHOOK] Method:", c.Method())
	log.Println("[PADDLE WEBHOOK] URL:", c.OriginalURL())
	log.Println("[PADDLE WEBHOOK] Signature Header:", c.Get("Paddle-Signature"))
	log.Println("[PADDLE WEBHOOK] Raw Body:", string(rawBody))
	log.Println("========================================")

	signatureHeader := strings.TrimSpace(c.Get("Paddle-Signature"))
	if signatureHeader == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: Missing Paddle-Signature header")
		return c.Status(401).SendString("Missing signature header")
	}

	parts := strings.Split(signatureHeader, ";")
	if len(parts) != 2 {
		log.Println("[PADDLE WEBHOOK] ERROR: Invalid signature format:", signatureHeader)
		return c.Status(401).SendString("Invalid signature format")
	}

	tsPart := strings.TrimPrefix(strings.TrimSpace(parts[0]), "ts=")
	h1Part := strings.TrimPrefix(strings.TrimSpace(parts[1]), "h1=")
	if tsPart == "" || h1Part == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: Invalid signature values")
		log.Println("[PADDLE WEBHOOK] ts =", tsPart)
		log.Println("[PADDLE WEBHOOK] h1 =", h1Part)
		return c.Status(401).SendString("Invalid signature format")
	}

	secretKey := strings.TrimSpace(os.Getenv("PADDLE_WEBHOOK_SECRET"))
	if secretKey == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: PADDLE_WEBHOOK_SECRET not configured")
		return c.Status(500).SendString("Webhook secret not configured")
	}

	log.Println("[PADDLE WEBHOOK] Secret loaded: YES")

	signedPayload := tsPart + ":" + string(rawBody)

	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write([]byte(signedPayload))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	log.Println("[PADDLE WEBHOOK] Timestamp:", tsPart)
	log.Println("[PADDLE WEBHOOK] Received H1:", h1Part)
	log.Println("[PADDLE WEBHOOK] Expected H1:", expectedHash)

	if !hmac.Equal([]byte(h1Part), []byte(expectedHash)) {
		log.Println("[PADDLE WEBHOOK] ERROR: Signature verification FAILED")
		return c.Status(401).SendString("Signature verification failed")
	}

	log.Println("[PADDLE WEBHOOK] Signature verified successfully")

	var payload PaddleWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		log.Println("[PADDLE WEBHOOK] ERROR: JSON decode failed:", err)
		return c.Status(400).SendString("Invalid webhook data format")
	}

	subscriptionID := firstNonEmpty(payload.Data.SubscriptionID, payload.Data.ID)
	userID := strings.TrimSpace(payload.Data.CustomData.UserID)

	log.Println("[PADDLE WEBHOOK] Event ID:", payload.EventID)
	log.Println("[PADDLE WEBHOOK] Event Type:", payload.EventType)
	log.Println("[PADDLE WEBHOOK] Data ID:", payload.Data.ID)
	log.Println("[PADDLE WEBHOOK] Customer ID:", payload.Data.CustomerID)
	log.Println("[PADDLE WEBHOOK] Subscription ID:", subscriptionID)
	log.Println("[PADDLE WEBHOOK] Status:", payload.Data.Status)
	log.Println("[PADDLE WEBHOOK] User ID:", userID)
	log.Println("[PADDLE WEBHOOK] Package Type:", payload.Data.CustomData.PackageType)
	log.Println("[PADDLE WEBHOOK] Billing Interval:", payload.Data.CustomData.BillingInterval)
	log.Println("[PADDLE WEBHOOK] Purchase Type:", payload.Data.CustomData.PurchaseType)
	log.Println("[PADDLE WEBHOOK] Billing Cycle Interval:", payload.Data.BillingCycle.Interval)
	log.Println("[PADDLE WEBHOOK] Trial Ends At:", payload.Data.TrialDates.EndsAt)
	log.Println("[PADDLE WEBHOOK] Next Billed At:", payload.Data.NextBilledAt)
	log.Println("[PADDLE WEBHOOK] Current Billing Period Ends At:", payload.Data.CurrentBillingPeriod.EndsAt)
	log.Println("[PADDLE WEBHOOK] Billing Period Ends At:", payload.Data.BillingPeriod.EndsAt)

	if strings.TrimSpace(payload.EventID) == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: Missing event_id")
		return c.Status(400).SendString("Missing webhook event_id")
	}

	var existingLog config.WebhookLog
	if err := config.DB.Where("event_id = ?", payload.EventID).First(&existingLog).Error; err == nil {
		log.Println("[PADDLE WEBHOOK] Duplicate event ignored:", payload.EventID)
		return c.Status(200).SendString("Webhook already processed (Idempotent)")
	}

	now := time.Now()
	var sub config.Subscription

	switch payload.EventType {
	case "subscription.created", "subscription.trialing", "subscription.activated", "subscription.updated":
		log.Println("[PADDLE WEBHOOK] Processing subscription event")

		if userID == "" {
			log.Println("[PADDLE WEBHOOK] ERROR: missing user_id in custom_data")
			return c.Status(400).SendString("Missing user_id in custom_data")
		}

		err := config.DB.Where("user_id = ?", userID).First(&sub).Error
		if err != nil && subscriptionID != "" {
			err = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
		}
		if err != nil {
			sub.ID = uuid.New().String()
			sub.UserID = userID
			sub.CreatedAt = now
		}

		if customerID := strings.TrimSpace(payload.Data.CustomerID); customerID != "" {
			sub.PaddleCustomerID = customerID
		}
		if subscriptionID != "" {
			sub.PaddleSubscriptionID = subscriptionID
		}

		status := strings.ToLower(strings.TrimSpace(payload.Data.Status))
		if status == "" {
			status = "active"
		}
		sub.Status = status

		interval := strings.ToLower(strings.TrimSpace(payload.Data.CustomData.BillingInterval))
		if interval == "" {
			interval = strings.ToLower(strings.TrimSpace(payload.Data.BillingCycle.Interval))
		}
		if interval == "" {
			interval = "monthly"
		}
		sub.BillingInterval = interval

		end := chooseSubscriptionEnd(payload, now)
		sub.CurrentPeriodEnd = end
		sub.UpdateURL = payload.Data.ManagementURLs.UpdatePaymentMethod
		sub.CancelURL = payload.Data.ManagementURLs.Cancel

		switch {
		case strings.Contains(strings.ToLower(payload.Data.CustomData.PackageType), "plus"):
			sub.Tier = "plus"
		case strings.Contains(strings.ToLower(payload.Data.CustomData.PackageType), "pro"):
			sub.Tier = "pro"
		case sub.Tier == "":
			sub.Tier = "free"
		}

		resetBillingWindows(&sub, now)
		sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
		sub.UpdatedAt = now

		if err := config.DB.Save(&sub).Error; err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR: Failed to save subscription state:", err)
			return c.Status(500).SendString("Failed to save subscription state")
		}

		log.Println("[PADDLE WEBHOOK] Subscription saved successfully")

	case "subscription.canceled", "subscription.paused", "subscription.past_due":
		log.Println("[PADDLE WEBHOOK] Processing subscription cancellation/past_due/paused")

		if userID != "" {
			if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil && subscriptionID != "" {
				_ = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
			}
		} else if subscriptionID != "" {
			_ = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
		}

		if sub.ID != "" {
			sub.Status = strings.ToLower(strings.TrimSpace(payload.Data.Status))
			if sub.Status == "" {
				sub.Status = "canceled"
			}

			end := chooseSubscriptionEnd(payload, now)
			if !end.IsZero() {
				sub.CurrentPeriodEnd = end
			}
			sub.UpdatedAt = now

			if err := config.DB.Save(&sub).Error; err != nil {
				log.Println("[PADDLE WEBHOOK] ERROR: Failed to save cancellation state:", err)
				return c.Status(500).SendString("Failed to save cancellation state")
			}

			log.Println("[PADDLE WEBHOOK] Subscription status updated to:", sub.Status)
		} else {
			log.Println("[PADDLE WEBHOOK] WARNING: No subscription found for cancellation event")
		}
	case "transaction.completed":
		log.Println("[PADDLE WEBHOOK] Processing completed transaction")

		amount := paddleTransactionAmount(payload)
		currency := firstNonEmpty(
			payload.Data.Details.Totals.CurrencyCode,
			payload.Data.CurrencyCode,
		)

		log.Println("[PADDLE WEBHOOK] Amount:", amount)
		log.Println("[PADDLE WEBHOOK] Currency:", currency)

		if userID == "" {
			log.Println("[PADDLE WEBHOOK] Missing user id")
			break
		}

		if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
			log.Println("[PADDLE WEBHOOK] Subscription row not found:", err)
			break
		}

		purchaseType := strings.ToLower(strings.TrimSpace(payload.Data.CustomData.PurchaseType))

		switch purchaseType {

		case "credits":

			packUnits := packageUnits(payload.Data.CustomData.PackageType)

			log.Println("[PADDLE WEBHOOK] Credit purchase")
			log.Println("[PADDLE WEBHOOK] Credits:", packUnits)

			if packUnits > 0 {
				sub.CustomCredits += packUnits
				sub.UpdatedAt = now

				if err := config.DB.Save(&sub).Error; err != nil {
					log.Println(err)
					return c.Status(500).SendString("failed to update credits")
				}
			}

		case "subscription":
			log.Println("[PADDLE WEBHOOK] Subscription purchase")
			sub.Status = "active"
			sub.PaddleCustomerID = payload.Data.CustomerID
			if payload.Data.SubscriptionID != "" {
				sub.PaddleSubscriptionID = payload.Data.SubscriptionID
			}
			sub.BillingInterval = payload.Data.CustomData.BillingInterval
			sub.CurrentPeriodEnd = chooseSubscriptionEnd(payload, now)
			sub.UpdatedAt = now
			switch strings.ToLower(payload.Data.CustomData.PackageType) {
			case "plus":
				sub.Tier = "plus"
			case "pro":
				sub.Tier = "pro"
			}
			resetBillingWindows(&sub, now)
			sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd

			if err := config.DB.Save(&sub).Error; err != nil {
				log.Println(err)
				return c.Status(500).SendString("failed to update subscription")
			}

			log.Println("[PADDLE WEBHOOK] Subscription activated")
		}
		tx := config.Transaction{
			ID:                  uuid.New().String(),
			UserID:              sub.UserID,
			SubscriptionID:      sub.ID,
			PaddleTransactionID: payload.Data.ID,
			Amount:              amount,
			Currency:            currency,
			Status:              "completed",
			CreatedAt:           now,
		}

		log.Println("[PADDLE WEBHOOK] " + strconv.FormatFloat(tx.Amount, 'a', 2, 64))

		if err := config.DB.Create(&tx).Error; err != nil {
			log.Println(err)
			return c.Status(500).SendString("failed to save transaction")
		}

		log.Println("[PADDLE WEBHOOK] Transaction saved")

	default:
		log.Println("[PADDLE WEBHOOK] Unhandled event type:", payload.EventType)
	}

	if err := config.DB.Create(&config.WebhookLog{
		ID:        uuid.New().String(),
		EventID:   payload.EventID,
		EventType: payload.EventType,
		Status:    "processed",
		CreatedAt: now,
	}).Error; err != nil {
		log.Println("[PADDLE WEBHOOK] ERROR: Failed to record webhook log:", err)
		return c.Status(500).SendString("Failed to record webhook log")
	}

	log.Println("[PADDLE WEBHOOK] Completed successfully")
	return c.Status(200).SendString("Webhook processed accurately.")
}

func chooseSubscriptionEnd(payload PaddleWebhookPayload, now time.Time) time.Time {
	for _, t := range []time.Time{
		payload.Data.CurrentBillingPeriod.EndsAt,
		payload.Data.BillingPeriod.EndsAt,
		payload.Data.TrialDates.EndsAt,
		payload.Data.NextBilledAt,
	} {
		if !t.IsZero() {
			return t
		}
	}

	switch strings.ToLower(strings.TrimSpace(payload.Data.BillingCycle.Interval)) {
	case "year", "yearly":
		return now.AddDate(1, 0, 0)
	default:
		return now.AddDate(0, 1, 0)
	}
}

func paddleTransactionAmount(payload PaddleWebhookPayload) float64 {
	for _, raw := range []string{
		payload.Data.Details.Totals.GrandTotal,
		payload.Data.Details.Totals.Total,
	} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			return n / 100.0
		}
	}
	return 0
}

func (ctrl *Controller) GetSubscriptionStatus(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	role, _ := c.Locals("role").(string)

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription data not found"})
	}

	limits := limitsForTier(sub.Tier)
	syncWindows(&sub, time.Now())

	threeHourRemaining := max(limits.Units3H-sub.UsedUnits3h, 0)

	dailyRemaining := max(limits.UnitsDay-sub.UsedUnitsDaily, 0)

	monthlyRemaining := max(limits.UnitsMonth-sub.UsedUnitsMonthly, 0)

	return c.JSON(fiber.Map{
		"tier":               sub.Tier,
		"status":             sub.Status,
		"current_period_end": sub.CurrentPeriodEnd,
		"custom_credits":     sub.CustomCredits,
		"update_url":         sub.UpdateURL,
		"cancel_url":         sub.CancelURL,
		"role":               role,
		"used_units_3h":      sub.UsedUnits3h,
		"used_units_daily":   sub.UsedUnitsDaily,
		"used_units_monthly": sub.UsedUnitsMonthly,

		"three_hour_limit": limits.Units3H + sub.CustomCredits,
		"daily_limit":      limits.UnitsDay + sub.CustomCredits,
		"monthly_limit":    limits.UnitsMonth + sub.CustomCredits,

		"three_hour_remaining": threeHourRemaining + sub.CustomCredits,
		"daily_remaining":      dailyRemaining + sub.CustomCredits,
		"monthly_remaining":    monthlyRemaining + sub.CustomCredits,

		"window_3h_reset_at":    sub.Window3HResetAt,
		"window_daily_reset_at": sub.WindowDailyResetAt,
		"window_month_reset_at": sub.WindowMonthlyResetAt,
	})
}

func (ctrl *Controller) GetTransactionHistory(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	var transactions []config.Transaction
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Find(&transactions)
	log.Println(transactions)
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
	case strings.Contains(pack, "addon_pack_200"):
		return 200
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
