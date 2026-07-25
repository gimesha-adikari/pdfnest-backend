package billing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

type webhookEnvelope struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
}

type subscriptionWebhookData struct {
	ID             string `json:"id"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id"`
	Status         string `json:"status"`
	CurrencyCode   string `json:"currency_code"`

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

	ManagementURLs struct {
		UpdatePaymentMethod string `json:"update_payment_method"`
		Cancel              string `json:"cancel"`
	} `json:"management_urls"`
}

type transactionWebhookData struct {
	ID             string `json:"id"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id"`
	Status         string `json:"status"`
	CurrencyCode   string `json:"currency_code"`

	CustomData struct {
		UserID          string `json:"user_id"`
		PackageType     string `json:"package_type"`
		BillingInterval string `json:"billing_interval"`
		PurchaseType    string `json:"purchase_type"`
	} `json:"custom_data"`

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
		return c.Status(fiber.StatusUnauthorized).SendString("Missing signature header")
	}

	parts := strings.Split(signatureHeader, ";")
	if len(parts) != 2 {
		log.Println("[PADDLE WEBHOOK] ERROR: Invalid signature format:", signatureHeader)
		return c.Status(fiber.StatusUnauthorized).SendString("Invalid signature format")
	}

	tsPart := strings.TrimPrefix(strings.TrimSpace(parts[0]), "ts=")
	h1Part := strings.TrimPrefix(strings.TrimSpace(parts[1]), "h1=")
	if tsPart == "" || h1Part == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: Invalid signature values")
		return c.Status(fiber.StatusUnauthorized).SendString("Invalid signature format")
	}

	secretKey := strings.TrimSpace(os.Getenv("PADDLE_WEBHOOK_SECRET"))
	if secretKey == "" {
		log.Println("[PADDLE WEBHOOK] ERROR: PADDLE_WEBHOOK_SECRET not configured")
		return c.Status(fiber.StatusInternalServerError).SendString("Webhook secret not configured")
	}

	signedPayload := tsPart + ":" + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write([]byte(signedPayload))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(h1Part), []byte(expectedHash)) {
		log.Println("[PADDLE WEBHOOK] ERROR: Signature verification FAILED")
		return c.Status(fiber.StatusUnauthorized).SendString("Signature verification failed")
	}

	var envelope webhookEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		log.Println("[PADDLE WEBHOOK] ERROR: JSON envelope decode failed:", err)
		return c.Status(fiber.StatusBadRequest).SendString("Invalid webhook data format")
	}

	if strings.TrimSpace(envelope.EventID) == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Missing webhook event_id")
	}

	var existingLog config.WebhookLog
	if err := config.DB.Where("event_id = ?", envelope.EventID).First(&existingLog).Error; err == nil {
		log.Println("[PADDLE WEBHOOK] Duplicate event ignored:", envelope.EventID)
		return c.Status(fiber.StatusOK).SendString("Webhook already processed (Idempotent)")
	}

	now := time.Now()

	switch envelope.EventType {
	case "subscription.created", "subscription.trialing", "subscription.activated", "subscription.updated":
		var data subscriptionWebhookData
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR: subscription payload decode failed:", err)
			return c.Status(fiber.StatusBadRequest).SendString("Invalid webhook data format")
		}
		if err := handleSubscriptionEvent(data, now); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR:", err)
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}

	case "subscription.canceled", "subscription.paused", "subscription.past_due":
		var data subscriptionWebhookData
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR: subscription cancel payload decode failed:", err)
			return c.Status(fiber.StatusBadRequest).SendString("Invalid webhook data format")
		}
		if err := handleSubscriptionCancellation(data, now); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR:", err)
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}

	case "transaction.completed":
		var data transactionWebhookData
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR: transaction payload decode failed:", err)
			return c.Status(fiber.StatusBadRequest).SendString("Invalid webhook data format")
		}
		if err := handleTransactionCompleted(data, now); err != nil {
			log.Println("[PADDLE WEBHOOK] ERROR:", err)
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}

	default:
		log.Println("[PADDLE WEBHOOK] Unhandled event type:", envelope.EventType)
	}

	if err := config.DB.Create(&config.WebhookLog{
		ID:        uuid.New().String(),
		EventID:   envelope.EventID,
		EventType: envelope.EventType,
		Status:    "processed",
		CreatedAt: now,
	}).Error; err != nil {
		log.Println("[PADDLE WEBHOOK] ERROR: Failed to record webhook log:", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to record webhook log")
	}

	log.Println("[PADDLE WEBHOOK] Completed successfully")
	return c.Status(fiber.StatusOK).SendString("Webhook processed accurately.")
}

func handleSubscriptionEvent(data subscriptionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	if userID == "" {
		return fmt.Errorf("missing user_id in custom_data")
	}

	subscriptionID := firstNonEmpty(data.SubscriptionID, data.ID)

	var sub config.Subscription
	err := config.DB.Where("user_id = ?", userID).First(&sub).Error
	if err != nil && subscriptionID != "" {
		err = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
	}

	isNew := false
	if err != nil {
		isNew = true
		sub = config.Subscription{
			ID:        uuid.New().String(),
			UserID:    userID,
			CreatedAt: now,
		}
	}

	if customerID := strings.TrimSpace(data.CustomerID); customerID != "" {
		sub.PaddleCustomerID = customerID
	}
	if subscriptionID != "" {
		sub.PaddleSubscriptionID = subscriptionID
	}

	status := strings.ToLower(strings.TrimSpace(data.Status))
	if status == "" {
		status = "active"
	}
	sub.Status = status

	interval := strings.ToLower(strings.TrimSpace(data.CustomData.BillingInterval))
	if interval == "" {
		interval = strings.ToLower(strings.TrimSpace(data.BillingCycle.Interval))
	}
	if interval == "" {
		interval = "monthly"
	}
	sub.BillingInterval = interval

	sub.CurrentPeriodEnd = chooseSubscriptionEndFromSubscription(data)
	sub.UpdateURL = data.ManagementURLs.UpdatePaymentMethod
	sub.CancelURL = data.ManagementURLs.Cancel

	switch {
	case strings.Contains(strings.ToLower(data.CustomData.PackageType), "plus"):
		sub.Tier = "plus"
	case strings.Contains(strings.ToLower(data.CustomData.PackageType), "pro"):
		sub.Tier = "pro"
	case sub.Tier == "":
		sub.Tier = "free"
	}

	resetBillingWindows(&sub, now)
	sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
	sub.UpdatedAt = now

	if isNew {
		return config.DB.Create(&sub).Error
	}
	return config.DB.Save(&sub).Error
}

func handleSubscriptionCancellation(data subscriptionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	subscriptionID := firstNonEmpty(data.SubscriptionID, data.ID)

	var sub config.Subscription
	var err error

	if userID != "" {
		err = config.DB.Where("user_id = ?", userID).First(&sub).Error
		if err != nil && subscriptionID != "" {
			err = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
		}
	} else if subscriptionID != "" {
		err = config.DB.Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
	}

	if err != nil || sub.ID == "" {
		log.Println("[PADDLE WEBHOOK] WARNING: No subscription found for cancellation event")
		return nil
	}

	sub.Status = strings.ToLower(strings.TrimSpace(data.Status))
	if sub.Status == "" {
		sub.Status = "canceled"
	}

	end := chooseSubscriptionEndFromSubscription(data)
	if !end.IsZero() {
		sub.CurrentPeriodEnd = end
	}
	sub.UpdatedAt = now

	return config.DB.Save(&sub).Error
}

func handleTransactionCompleted(data transactionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	if userID == "" {
		return fmt.Errorf("missing user id")
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		sub = config.Subscription{
			ID:        uuid.New().String(),
			UserID:    userID,
			Status:    "active",
			Tier:      "free",
			CreatedAt: now,
		}
	}

	amount := paddleTransactionAmountFromTransaction(data)
	currency := firstNonEmpty(data.Details.Totals.CurrencyCode, data.CurrencyCode)

	purchaseType := strings.ToLower(strings.TrimSpace(data.CustomData.PurchaseType))

	switch purchaseType {
	case "credits":
		packUnits := packageUnits(data.CustomData.PackageType)
		if packUnits > 0 {
			sub.CustomCredits += packUnits
			sub.UpdatedAt = now
			if err := saveOrCreateSubscription(&sub); err != nil {
				return fmt.Errorf("failed to update credits: %w", err)
			}
		}

	case "subscription":
		sub.Status = "active"
		sub.PaddleCustomerID = firstNonEmpty(sub.PaddleCustomerID, data.CustomerID)
		if data.SubscriptionID != "" {
			sub.PaddleSubscriptionID = data.SubscriptionID
		}
		sub.BillingInterval = normalizeBillingInterval(data.CustomData.BillingInterval)
		sub.CurrentPeriodEnd = chooseSubscriptionEndFromTransaction(data)
		switch strings.ToLower(strings.TrimSpace(data.CustomData.PackageType)) {
		case "plus":
			sub.Tier = "plus"
		case "pro":
			sub.Tier = "pro"
		}
		resetBillingWindows(&sub, now)
		sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
		sub.UpdatedAt = now

		if err := saveOrCreateSubscription(&sub); err != nil {
			return fmt.Errorf("failed to update subscription: %w", err)
		}

	default:
		// Even if purchase type is not recognized, save the transaction record below.
	}

	if err := config.DB.Save(&sub).Error; err != nil {
		return err
	}

	tx := config.Transaction{
		ID:                  uuid.New().String(),
		UserID:              sub.UserID,
		SubscriptionID:      sub.ID,
		PaddleTransactionID: data.ID,
		Amount:              amount,
		Currency:            currency,
		Status:              "completed",
		CreatedAt:           now,
	}

	if err := config.DB.Create(&tx).Error; err != nil {
		return err
	}

	return nil
}

func saveOrCreateSubscription(sub *config.Subscription) error {
	if sub.ID == "" {
		sub.ID = uuid.New().String()
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now()
	}
	if sub.UserID == "" {
		return fmt.Errorf("missing subscription user id")
	}

	var existing config.Subscription
	err := config.DB.Where("id = ?", sub.ID).First(&existing).Error
	if err != nil {
		if strings.TrimSpace(sub.PaddleSubscriptionID) != "" {
			err = config.DB.Where("paddle_subscription_id = ?", sub.PaddleSubscriptionID).First(&existing).Error
		}
	}
	if err == nil {
		sub.ID = existing.ID
		return config.DB.Save(sub).Error
	}
	return config.DB.Create(sub).Error
}

func chooseSubscriptionEndFromSubscription(data subscriptionWebhookData) time.Time {
	for _, t := range []time.Time{
		data.CurrentBillingPeriod.EndsAt,
		data.BillingPeriod.EndsAt,
		data.TrialDates.EndsAt,
		data.NextBilledAt,
	} {
		if !t.IsZero() {
			return t
		}
	}

	switch strings.ToLower(strings.TrimSpace(data.BillingCycle.Interval)) {
	case "year", "yearly":
		return time.Now().AddDate(1, 0, 0)
	default:
		return time.Now().AddDate(0, 1, 0)
	}
}

func chooseSubscriptionEndFromTransaction(data transactionWebhookData) time.Time {
	for _, t := range []time.Time{
		data.BillingPeriod.EndsAt,
	} {
		if !t.IsZero() {
			return t
		}
	}
	return time.Now().AddDate(0, 1, 0)
}

func paddleTransactionAmountFromTransaction(data transactionWebhookData) float64 {
	for _, raw := range []string{
		data.Details.Totals.GrandTotal,
		data.Details.Totals.Total,
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

func normalizeBillingInterval(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "monthly"
	}
	if v == "year" {
		return "yearly"
	}
	return v
}

func (ctrl *Controller) GetSubscriptionStatus(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	role, _ := c.Locals("role").(string)

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Subscription data not found"})
	}

	limits := limitsForTier(sub.Tier)
	syncWindows(&sub, time.Now())

	threeHourRemaining := maxInt(limits.Units3H-sub.UsedUnits3h, 0)
	dailyRemaining := maxInt(limits.UnitsDay-sub.UsedUnitsDaily, 0)
	monthlyRemaining := maxInt(limits.UnitsMonth-sub.UsedUnitsMonthly, 0)

	return c.JSON(fiber.Map{
		"tier":                  sub.Tier,
		"status":                sub.Status,
		"billing_interval":      sub.BillingInterval,
		"current_period_end":    sub.CurrentPeriodEnd,
		"custom_credits":        sub.CustomCredits,
		"update_url":            sub.UpdateURL,
		"cancel_url":            sub.CancelURL,
		"role":                  role,
		"used_units_3h":         sub.UsedUnits3h,
		"used_units_daily":      sub.UsedUnitsDaily,
		"used_units_monthly":    sub.UsedUnitsMonthly,
		"three_hour_limit":      limits.Units3H + sub.CustomCredits,
		"daily_limit":           limits.UnitsDay + sub.CustomCredits,
		"monthly_limit":         limits.UnitsMonth + sub.CustomCredits,
		"three_hour_remaining":  threeHourRemaining + sub.CustomCredits,
		"daily_remaining":       dailyRemaining + sub.CustomCredits,
		"monthly_remaining":     monthlyRemaining + sub.CustomCredits,
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
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Subscription row registry not found"})
	}

	now := time.Now()

	sub.Tier = req.Tier
	sub.Status = "active"
	sub.CurrentPeriodEnd = now.AddDate(0, 1, 0)
	sub.UpdateURL = "https://sandbox.paddle.com/mock-update"
	sub.CancelURL = "https://sandbox.paddle.com/mock-cancel"

	resetBillingWindows(&sub, now)
	sub.WindowMonthlyResetAt = sub.CurrentPeriodEnd
	sub.UsedUnitsMonthly = 0
	sub.UpdatedAt = now

	if err := config.DB.Save(&sub).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save upgraded subscription"})
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
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to record upgrade transaction"})
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid target payload credits definition amount value."})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Subscription record missing."})
	}

	now := time.Now()
	sub.CustomCredits += req.Credits
	sub.UpdatedAt = now

	if err := config.DB.Save(&sub).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to add credits"})
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
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to record credit transaction"})
	}

	return c.JSON(fiber.Map{
		"status":         "success",
		"custom_credits": sub.CustomCredits,
	})
}

type PaddlePortalSessionResponse struct {
	Data struct {
		ID         string `json:"id"`
		CustomerID string `json:"customer_id"`
		URLs       struct {
			General struct {
				Overview string `json:"overview"`
			} `json:"general"`
			Subscriptions []struct {
				ID                              string `json:"id"`
				CancelSubscription              string `json:"cancel_subscription"`
				UpdateSubscriptionPaymentMethod string `json:"update_subscription_payment_method"`
			} `json:"subscriptions"`
		} `json:"urls"`
	} `json:"data"`
}

func paddleAPIBaseURL() string {
	base := strings.TrimSpace(os.Getenv("PADDLE_API_BASE_URL"))
	if base == "" {
		base = "https://sandbox-api.paddle.com"
	}
	return strings.TrimRight(base, "/")
}

func (ctrl *Controller) CreatePortalSession(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Subscription not found"})
	}

	if !strings.HasPrefix(sub.PaddleCustomerID, "ctm_") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Billing portal is only available for paid Paddle customers.",
		})
	}

	apiKey := strings.TrimSpace(os.Getenv("PADDLE_API_KEY"))
	if apiKey == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "PADDLE_API_KEY is not configured",
		})
	}

	reqBody := map[string]any{}
	if strings.HasPrefix(sub.PaddleSubscriptionID, "sub_") {
		reqBody["subscription_ids"] = []string{sub.PaddleSubscriptionID}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to build portal request",
		})
	}

	endpoint := fmt.Sprintf("%s/customers/%s/portal-sessions", paddleAPIBaseURL(), sub.PaddleCustomerID)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to create portal request",
		})
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Paddle-Version", "1")

	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "Failed to contact Paddle",
		})
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	log.Println(string(body))

	if res.StatusCode != http.StatusCreated {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "Paddle portal session failed",
			"details": string(body),
		})
	}

	var decoded PaddlePortalSessionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to parse Paddle response",
		})
	}

	resp := fiber.Map{
		"overview_url": decoded.Data.URLs.General.Overview,
	}

	if len(decoded.Data.URLs.Subscriptions) > 0 {
		resp["update_payment_url"] = decoded.Data.URLs.Subscriptions[0].UpdateSubscriptionPaymentMethod
		resp["cancel_subscription_url"] = decoded.Data.URLs.Subscriptions[0].CancelSubscription
	}

	return c.JSON(resp)
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
