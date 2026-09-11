package billing

import (
	"bytes"
	"context"
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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	if err := verifyWebhookSignature(c.Get("Paddle-Signature"), rawBody, os.Getenv("PADDLE_WEBHOOK_SECRET"), time.Now()); err != nil {
		log.Println("[PADDLE WEBHOOK] signature rejected")
		return c.Status(fiber.StatusUnauthorized).SendString("Invalid webhook signature")
	}
	var envelope webhookEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil || strings.TrimSpace(envelope.EventID) == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid webhook event")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 4*time.Second)
	defer cancel()
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Claim and effects commit together. A failed transaction releases the
		// event ID so Paddle can retry; concurrent duplicates wait for commit.
		claim := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true}).Create(&config.WebhookLog{
			ID: uuid.NewString(), EventID: envelope.EventID, EventType: envelope.EventType, Status: "processed", CreatedAt: time.Now(),
		})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return nil
		}
		switch envelope.EventType {
		case "subscription.created", "subscription.trialing", "subscription.activated", "subscription.updated":
			var data subscriptionWebhookData
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				return err
			}
			if err := lockBillingUser(tx, data.CustomData.UserID); err != nil {
				return err
			}
			return handleSubscriptionEvent(tx, data, time.Now())
		case "subscription.canceled", "subscription.paused", "subscription.past_due":
			var data subscriptionWebhookData
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				return err
			}
			if data.CustomData.UserID != "" {
				if err := lockBillingUser(tx, data.CustomData.UserID); err != nil {
					return err
				}
			}
			return handleSubscriptionCancellation(tx, data, time.Now())
		case "transaction.completed":
			var data transactionWebhookData
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				return err
			}
			if err := lockBillingUser(tx, data.CustomData.UserID); err != nil {
				return err
			}
			return handleTransactionCompleted(tx, data, time.Now())
		default:
			return nil
		}
	})
	if err != nil {
		log.Printf("[PADDLE WEBHOOK] event_id=%s type=%s persistence failed", envelope.EventID, envelope.EventType)
		return c.Status(fiber.StatusInternalServerError).SendString("Webhook could not be committed; retry delivery")
	}
	log.Printf("[PADDLE WEBHOOK] event_id=%s type=%s committed", envelope.EventID, envelope.EventType)
	return c.SendString("Webhook processed")
}

func lockBillingUser(db *gorm.DB, userID string) error {
	var user config.User
	return db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&user, "id = ?", strings.TrimSpace(userID)).Error
}

func verifyWebhookSignature(header string, raw []byte, secret string, now time.Time) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("webhook secret unavailable")
	}
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return fmt.Errorf("invalid signature header")
		}
		switch name {
		case "ts":
			timestamp = value
		case "h1":
			signatures = append(signatures, value)
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	// Paddle SDKs use a five-second delivery timestamp tolerance. This is
	// separate from event occurrence time; every delivery receives a new stamp.
	if err != nil || seconds < now.Unix()-5 || seconds > now.Unix()+5 {
		return fmt.Errorf("expired signature")
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(timestamp + ":"))
	_, _ = mac.Write(raw)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		actual, err := hex.DecodeString(signature)
		if err == nil && hmac.Equal(actual, expected) {
			return nil
		}
	}
	return fmt.Errorf("signature mismatch")
}

func handleSubscriptionEvent(db *gorm.DB, data subscriptionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	if userID == "" {
		return fmt.Errorf("missing user_id in custom_data")
	}

	subscriptionID := firstNonEmpty(data.SubscriptionID, data.ID)

	var sub config.Subscription
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&sub).Error
	if err != nil && subscriptionID != "" {
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
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
		return db.Create(&sub).Error
	}
	return db.Save(&sub).Error
}

func handleSubscriptionCancellation(db *gorm.DB, data subscriptionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	subscriptionID := firstNonEmpty(data.SubscriptionID, data.ID)

	var sub config.Subscription
	var err error

	if userID != "" {
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&sub).Error
		if err != nil && subscriptionID != "" {
			err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
		}
	} else if subscriptionID != "" {
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("paddle_subscription_id = ?", subscriptionID).First(&sub).Error
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

	return db.Save(&sub).Error
}

func handleTransactionCompleted(db *gorm.DB, data transactionWebhookData, now time.Time) error {
	userID := strings.TrimSpace(data.CustomData.UserID)
	if userID == "" {
		return fmt.Errorf("missing user id")
	}

	var sub config.Subscription
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&sub).Error; err != nil {
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
			if err := saveOrCreateSubscription(db, &sub); err != nil {
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

		if err := saveOrCreateSubscription(db, &sub); err != nil {
			return fmt.Errorf("failed to update subscription: %w", err)
		}

	default:
		// Even if purchase type is not recognized, save the transaction record below.
	}

	if err := db.Save(&sub).Error; err != nil {
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

	if err := db.Create(&tx).Error; err != nil {
		return err
	}

	return nil
}

func saveOrCreateSubscription(db *gorm.DB, sub *config.Subscription) error {
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
	err := db.Where("id = ?", sub.ID).First(&existing).Error
	if err != nil {
		if strings.TrimSpace(sub.PaddleSubscriptionID) != "" {
			err = db.Where("paddle_subscription_id = ?", sub.PaddleSubscriptionID).First(&existing).Error
		}
	}
	if err == nil {
		sub.ID = existing.ID
		return db.Save(sub).Error
	}
	return db.Create(sub).Error
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
		return billingLimits{Units3H: 10, UnitsDay: 20, UnitsMonth: 80}
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
