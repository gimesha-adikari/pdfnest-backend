package billing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func signedWebhook(t *testing.T, app *fiber.App, body []byte) int {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("test-webhook-secret"))
	mac.Write([]byte(timestamp + ":"))
	mac.Write(body)
	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Paddle-Signature", "ts="+timestamp+";h1="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := app.Test(req, 10000)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestWebhookFailureCannotPartiallyGrantCredits(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&config.Transaction{}, &config.WebhookLog{}))
	userID, subID := createTestUserAndSub(db)
	t.Setenv("PADDLE_WEBHOOK_SECRET", "test-webhook-secret")
	eventID := "evt_" + uuid.NewString()
	body, err := json.Marshal(map[string]any{"event_id": eventID, "event_type": "transaction.completed", "data": map[string]any{"id": "txn_" + uuid.NewString(), "custom_data": map[string]any{"user_id": userID, "purchase_type": "credits", "package_type": "addon_pack_20"}}})
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/webhook", NewController().HandleWebhook)
	callback := "audit_fail_event_record_" + uuid.NewString()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "webhook_logs" {
			tx.AddError(errors.New("controlled event persistence failure"))
		}
	}))
	require.Equal(t, 500, signedWebhook(t, app, body))
	require.NoError(t, db.Callback().Create().Remove(callback))
	var sub config.Subscription
	require.NoError(t, db.First(&sub, "id = ?", subID).Error)
	require.Zero(t, sub.CustomCredits, "failed receipt must not commit the entitlement mutation")
	var count int64
	require.NoError(t, db.Model(&config.Transaction{}).Where("user_id = ?", userID).Count(&count).Error)
	require.Zero(t, count)
	var wg sync.WaitGroup
	codes := make(chan int, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- signedWebhook(t, app, body) }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		require.Equal(t, 200, code)
	}
	require.NoError(t, db.First(&sub, "id = ?", subID).Error)
	require.Equal(t, 20, sub.CustomCredits)
	require.NoError(t, db.Model(&config.Transaction{}).Where("user_id = ?", userID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestWebhookDoesNotLogPayloadOrSignature(t *testing.T) {
	var capture bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&capture)
	defer log.SetOutput(previous)
	app := fiber.New()
	app.Post("/webhook", NewController().HandleWebhook)
	req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString("private-payload-sentinel"))
	req.Header.Set("Paddle-Signature", "private-signature-sentinel")
	resp, err := app.Test(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 401, resp.StatusCode)
	require.NotContains(t, capture.String(), "private-payload-sentinel")
	require.NotContains(t, capture.String(), "private-signature-sentinel")
}

func TestWebhookStillProcessesPreexistingPurchaseInFreeMode(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_WEBHOOK_SECRET", "test-webhook-secret")
	db := setupGIM6IsolatedTestDB(t)
	require.NoError(t, db.AutoMigrate(&config.Transaction{}, &config.WebhookLog{}))
	userID := createCheckoutTestUser(t, db)
	subscriptionID := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&config.Subscription{
		ID: subscriptionID, UserID: userID, PaddleCustomerID: "ctm_" + uuid.NewString(),
		PaddleSubscriptionID: "sub_" + uuid.NewString(), Tier: "free", Status: "active",
		CurrentPeriodEnd: now.AddDate(0, 1, 0), Window3HResetAt: now.Add(time.Hour),
		WindowDailyResetAt: now.Add(24 * time.Hour), WindowMonthlyResetAt: now.AddDate(0, 1, 0),
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	eventID := "evt_" + uuid.NewString()
	body, err := json.Marshal(map[string]any{
		"event_id":   eventID,
		"event_type": "transaction.completed",
		"data": map[string]any{
			"id":            "txn_" + uuid.NewString(),
			"customer_id":   "ctm_test",
			"currency_code": "USD",
			"custom_data": map[string]any{
				"user_id":       userID,
				"purchase_type": "credits",
				"package_type":  "addon_pack_20",
			},
			"details": map[string]any{"totals": map[string]any{"total": "500", "currency_code": "USD"}},
		},
	})
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/webhook", NewController().HandleWebhook)

	require.Equal(t, http.StatusOK, signedWebhook(t, app, body))
	var subscription config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
	require.Equal(t, 20, subscription.CustomCredits, "a preexisting transaction must still synchronize in free mode")
	var transactionCount, eventCount int64
	require.NoError(t, db.Model(&config.Transaction{}).Where("user_id = ?", userID).Count(&transactionCount).Error)
	require.NoError(t, db.Model(&config.WebhookLog{}).Where("event_id = ?", eventID).Count(&eventCount).Error)
	require.EqualValues(t, 1, transactionCount)
	require.EqualValues(t, 1, eventCount)
}

func TestWebhookSignatureFreshnessAndRotation(t *testing.T) {
	now := time.Now()
	body := []byte(`{"event_id":"controlled"}`)
	sign := func(seconds int64) string {
		stamp := strconv.FormatInt(seconds, 10)
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(stamp + ":"))
		mac.Write(body)
		return "ts=" + stamp + ";h1=" + hex.EncodeToString(mac.Sum(nil))
	}
	require.NoError(t, verifyWebhookSignature(sign(now.Unix()), body, "secret", now))
	require.NoError(t, verifyWebhookSignature(sign(now.Unix())+";h1=00", body, "secret", now))
	require.Error(t, verifyWebhookSignature(sign(now.Unix()-60), body, "secret", now))
	require.Error(t, verifyWebhookSignature(sign(now.Unix()+60), body, "secret", now))
	require.Error(t, verifyWebhookSignature(sign(now.Unix()), []byte("changed"), "secret", now))
	require.Error(t, verifyWebhookSignature(sign(now.Unix()), body, "", now))
}
