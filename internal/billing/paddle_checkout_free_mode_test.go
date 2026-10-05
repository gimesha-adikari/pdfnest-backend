package billing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/gofiber/fiber/v2"
	recovermw "github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type capturedPaddleRequest struct {
	path          string
	authorization string
	body          []byte
}

func newCheckoutPaddleServer(t *testing.T) (*httptest.Server, *atomic.Int64, <-chan capturedPaddleRequest) {
	t.Helper()
	var requestCount atomic.Int64
	requests := make(chan capturedPaddleRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		requests <- capturedPaddleRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if strings.Contains(r.URL.Path, "/portal-sessions") {
			_, _ = io.WriteString(w, `{"data":{"id":"prt_test","customer_id":"ctm_test","urls":{"general":{"overview":"https://portal.example.test"},"subscriptions":[{"id":"sub_test","cancel_subscription":"https://portal.example.test/cancel","update_subscription_payment_method":"https://portal.example.test/update"}]}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"txn_test","checkout":{"url":"https://checkout.example.test/session"}}}`)
	}))
	t.Cleanup(server.Close)
	return server, &requestCount, requests
}

func checkoutTestApp(ctrl *Controller, userID string) *fiber.App {
	app := fiber.New()
	app.Use(recovermw.New())
	if userID != "" {
		app.Use(func(c *fiber.Ctx) error {
			c.Locals("user_id", userID)
			return c.Next()
		})
	}
	app.Post("/billing/checkout", ctrl.CreateCheckout)
	app.Post("/billing/checkout-credits", ctrl.CreateCreditCheckout)
	return app
}

func performCheckoutRequest(t *testing.T, app *fiber.App, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func TestCreateSubscriptionCheckoutRejectedBeforeBillingOrPaddleInFreeMode(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	var preparationCount atomic.Int64
	ctrl := &Controller{prepareCheckoutSubscription: func(string) (*config.Subscription, error) {
		preparationCount.Add(1)
		return &config.Subscription{}, nil
	}}
	app := checkoutTestApp(ctrl, "checkout-free-subscription-user")

	response := performCheckoutRequest(t, app, "/billing/checkout", `{"tier":"plus","interval":"monthly"}`)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusForbidden, response.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "PURCHASES_DISABLED", body["code"])
	require.Equal(t, "New purchases are currently disabled while processing is free.", body["message"])
	require.Zero(t, preparationCount.Load(), "free-mode rejection must precede subscription-row preparation")
	require.Zero(t, requestCount.Load(), "free-mode rejection must happen before Paddle transaction creation")
}

func TestCreateCreditCheckoutRejectedBeforeBillingOrPaddleInFreeMode(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_PRICE_CREDITS_10", "pri_credits_10_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	var preparationCount atomic.Int64
	ctrl := &Controller{prepareCheckoutSubscription: func(string) (*config.Subscription, error) {
		preparationCount.Add(1)
		return &config.Subscription{}, nil
	}}
	app := checkoutTestApp(ctrl, "checkout-free-credit-user")

	response := performCheckoutRequest(t, app, "/billing/checkout-credits", `{"credits":10}`)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusForbidden, response.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "PURCHASES_DISABLED", body["code"])
	require.Equal(t, "New purchases are currently disabled while processing is free.", body["message"])
	require.Zero(t, preparationCount.Load(), "free-mode rejection must precede subscription-row preparation")
	require.Zero(t, requestCount.Load(), "free-mode rejection must happen before Paddle transaction creation")
}

func TestCheckoutAuthenticationAndValidationPrecedePurchasePolicy(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	app := checkoutTestApp(NewController(), "")

	unauthorized := performCheckoutRequest(t, app, "/billing/checkout", `{"tier":"plus","interval":"monthly"}`)
	unauthorized.Body.Close()
	require.Equal(t, fiber.StatusUnauthorized, unauthorized.StatusCode)

	routedApp := fiber.New()
	RegisterRoutes(routedApp, NewController())
	routedUnauthorized := performCheckoutRequest(t, routedApp, "/billing/checkout", `{"tier":"plus","interval":"monthly"}`)
	routedUnauthorized.Body.Close()
	require.Equal(t, fiber.StatusUnauthorized, routedUnauthorized.StatusCode, "the registered checkout route remains behind authentication")

	app = checkoutTestApp(NewController(), "checkout-validation-user")
	malformedBody := performCheckoutRequest(t, app, "/billing/checkout", `{"tier":`)
	malformedBody.Body.Close()
	require.Equal(t, fiber.StatusBadRequest, malformedBody.StatusCode)

	invalidTier := performCheckoutRequest(t, app, "/billing/checkout", `{"tier":"enterprise","interval":"monthly"}`)
	invalidTier.Body.Close()
	require.Equal(t, fiber.StatusBadRequest, invalidTier.StatusCode)

	invalidCredits := performCheckoutRequest(t, app, "/billing/checkout-credits", `{"credits":11}`)
	invalidCredits.Body.Close()
	require.Equal(t, fiber.StatusBadRequest, invalidCredits.StatusCode)
}

func TestCheckoutFailsClosedWhenBillingPolicyIsInvalid(t *testing.T) {
	t.Setenv("BILLING_MODE", "unlimited")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	var preparationCount atomic.Int64
	ctrl := &Controller{prepareCheckoutSubscription: func(string) (*config.Subscription, error) {
		preparationCount.Add(1)
		return &config.Subscription{}, nil
	}}
	app := checkoutTestApp(ctrl, "checkout-invalid-policy-user")

	response := performCheckoutRequest(t, app, "/billing/checkout", `{"tier":"plus","interval":"monthly"}`)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusInternalServerError, response.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "BILLING_POLICY_UNAVAILABLE", body["code"])
	require.Zero(t, preparationCount.Load())
	require.Zero(t, requestCount.Load(), "invalid mode must fail closed before Paddle")
}

func TestCheckoutInFreeModeDoesNotCreateSubscriptionRows(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	t.Setenv("PADDLE_PRICE_CREDITS_10", "pri_credits_10_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	db := setupGIM6IsolatedTestDB(t)
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)

	for _, test := range []struct {
		name, path, body string
	}{
		{name: "subscription", path: "/billing/checkout", body: `{"tier":"plus","interval":"monthly"}`},
		{name: "credits", path: "/billing/checkout-credits", body: `{"credits":10}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			userID := createCheckoutTestUser(t, db)
			app := checkoutTestApp(NewController(), userID)
			response := performCheckoutRequest(t, app, test.path, test.body)
			response.Body.Close()
			require.Equal(t, fiber.StatusForbidden, response.StatusCode)
			var count int64
			require.NoError(t, db.Model(&config.Subscription{}).Where("user_id = ?", userID).Count(&count).Error)
			require.Zero(t, count, "policy rejection must precede ensureSubscriptionRow")
		})
	}
	require.Zero(t, requestCount.Load())
}

func TestFreeModeCheckoutDoesNotMutateExistingSubscriptionOrHistory(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	t.Setenv("PADDLE_PRICE_CREDITS_10", "pri_credits_10_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	db := setupGIM6IsolatedTestDB(t)
	require.NoError(t, db.AutoMigrate(&config.Transaction{}))
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	userID := createCheckoutTestUser(t, db)
	subscriptionID := uuid.NewString()
	now := time.Now()
	want := config.Subscription{
		ID: subscriptionID, UserID: userID, PaddleCustomerID: "ctm_" + uuid.NewString(),
		PaddleSubscriptionID: "sub_" + uuid.NewString(), Tier: "pro", Status: "active",
		BillingInterval: "yearly", CustomCredits: 120, UpdateURL: "https://manage.example.test/update",
		CancelURL: "https://manage.example.test/cancel", CurrentPeriodEnd: now.AddDate(0, 1, 0),
		UsedUnits3h: 3, UsedUnitsDaily: 5, UsedUnitsMonthly: 8,
		Window3HResetAt: now.Add(time.Hour), WindowDailyResetAt: now.Add(24 * time.Hour),
		WindowMonthlyResetAt: now.AddDate(0, 1, 0), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&want).Error)

	app := checkoutTestApp(NewController(), userID)
	for _, test := range []struct{ path, body string }{
		{path: "/billing/checkout", body: `{"tier":"plus","interval":"monthly"}`},
		{path: "/billing/checkout-credits", body: `{"credits":10}`},
	} {
		response := performCheckoutRequest(t, app, test.path, test.body)
		response.Body.Close()
		require.Equal(t, fiber.StatusForbidden, response.StatusCode)
	}

	var got config.Subscription
	require.NoError(t, db.First(&got, "id = ?", subscriptionID).Error)
	require.Equal(t, want.Tier, got.Tier)
	require.Equal(t, want.Status, got.Status)
	require.Equal(t, want.PaddleCustomerID, got.PaddleCustomerID)
	require.Equal(t, want.PaddleSubscriptionID, got.PaddleSubscriptionID)
	require.Equal(t, want.BillingInterval, got.BillingInterval)
	require.Equal(t, want.CustomCredits, got.CustomCredits)
	require.Equal(t, want.UpdateURL, got.UpdateURL)
	require.Equal(t, want.CancelURL, got.CancelURL)
	require.Equal(t, want.UsedUnits3h, got.UsedUnits3h)
	require.Equal(t, want.UsedUnitsDaily, got.UsedUnitsDaily)
	require.Equal(t, want.UsedUnitsMonthly, got.UsedUnitsMonthly)
	var transactionCount int64
	require.NoError(t, db.Model(&config.Transaction{}).Where("user_id = ?", userID).Count(&transactionCount).Error)
	require.Zero(t, transactionCount)
	require.Zero(t, requestCount.Load())
}

func TestNormalModeSubscriptionAndCreditCheckoutReachPaddle(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	t.Setenv("PADDLE_PRICE_CREDITS_10", "pri_credits_10_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	server, requestCount, requests := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	var preparationCount atomic.Int64
	ctrl := &Controller{prepareCheckoutSubscription: func(userID string) (*config.Subscription, error) {
		preparationCount.Add(1)
		return &config.Subscription{UserID: userID}, nil
	}}

	for _, test := range []struct {
		name, path, body, userID  string
		wantPackage, wantPurchase string
	}{
		{name: "subscription", path: "/billing/checkout", body: `{"tier":"plus","interval":"monthly"}`, userID: "normal-checkout-subscription-user", wantPackage: "plus", wantPurchase: "subscription"},
		{name: "credits", path: "/billing/checkout-credits", body: `{"credits":10}`, userID: "normal-checkout-credit-user", wantPackage: "addon_pack_10", wantPurchase: "credits"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := checkoutTestApp(ctrl, test.userID)
			response := performCheckoutRequest(t, app, test.path, test.body)
			defer response.Body.Close()
			require.Equal(t, fiber.StatusOK, response.StatusCode)
			var decoded map[string]string
			require.NoError(t, json.NewDecoder(response.Body).Decode(&decoded))
			require.Equal(t, "https://checkout.example.test/session", decoded["checkout_url"])

			captured := <-requests
			require.Equal(t, "/transactions", captured.path)
			require.Equal(t, "Bearer paddle-test-key", captured.authorization)
			var payload paddleTransactionCreatePayload
			require.NoError(t, json.Unmarshal(captured.body, &payload))
			require.Len(t, payload.Items, 1)
			require.Equal(t, 1, payload.Items[0].Quantity)
			if test.name == "subscription" {
				require.Equal(t, "pri_plus_monthly_test", payload.Items[0].PriceID)
			} else {
				require.Equal(t, "pri_credits_10_test", payload.Items[0].PriceID)
			}
			require.Equal(t, "automatic", payload.CollectionMode)
			require.Equal(t, test.userID, payload.CustomData["user_id"])
			require.Equal(t, test.wantPackage, payload.CustomData["package_type"])
			require.Equal(t, test.wantPurchase, payload.CustomData["purchase_type"])
			if test.name == "subscription" {
				require.Equal(t, "monthly", payload.CustomData["billing_interval"])
			}
		})
	}
	require.EqualValues(t, 2, preparationCount.Load())
	require.EqualValues(t, 2, requestCount.Load())
}

func TestCheckoutReadsCurrentPolicyForEachRequest(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	t.Setenv("PADDLE_PRICE_PLUS_MONTHLY", "pri_plus_monthly_test")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	server, requestCount, _ := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	var preparationCount atomic.Int64
	ctrl := &Controller{prepareCheckoutSubscription: func(string) (*config.Subscription, error) {
		preparationCount.Add(1)
		return &config.Subscription{}, nil
	}}
	app := checkoutTestApp(ctrl, "checkout-mode-transition-user")
	request := func() *http.Response {
		return performCheckoutRequest(t, app, "/billing/checkout", `{"tier":"plus","interval":"monthly"}`)
	}

	first := request()
	first.Body.Close()
	require.Equal(t, fiber.StatusOK, first.StatusCode)
	require.EqualValues(t, 1, requestCount.Load())

	t.Setenv("BILLING_MODE", "free")
	second := request()
	defer second.Body.Close()
	require.Equal(t, fiber.StatusForbidden, second.StatusCode)
	require.EqualValues(t, 1, preparationCount.Load())
	require.EqualValues(t, 1, requestCount.Load())
}

func TestCreatePortalSessionRemainsAvailableInFreeMode(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("PADDLE_API_KEY", "paddle-test-key")
	db := setupGIM6IsolatedTestDB(t)
	server, requestCount, requests := newCheckoutPaddleServer(t)
	t.Setenv("PADDLE_API_BASE_URL", server.URL)
	userID := createCheckoutTestUser(t, db)
	subscriptionID := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&config.Subscription{
		ID: subscriptionID, UserID: userID, PaddleCustomerID: "ctm_" + uuid.NewString(),
		PaddleSubscriptionID: "sub_" + uuid.NewString(), Tier: "pro", Status: "active",
		CurrentPeriodEnd: now.AddDate(0, 1, 0), Window3HResetAt: now.Add(time.Hour),
		WindowDailyResetAt: now.Add(24 * time.Hour), WindowMonthlyResetAt: now.AddDate(0, 1, 0),
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", userID); return c.Next() })
	app.Post("/billing/portal-session", NewController().CreatePortalSession)
	response := performCheckoutRequest(t, app, "/billing/portal-session", `{}`)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusOK, response.StatusCode)
	require.EqualValues(t, 1, requestCount.Load())
	captured := <-requests
	require.Contains(t, captured.path, "/customers/ctm_")
}

func createCheckoutTestUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	userID := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&config.User{ID: userID, Email: userID + "@example.test", CreatedAt: now, UpdatedAt: now}).Error)
	t.Cleanup(func() {
		_ = config.DB.Where("user_id = ?", userID).Delete(&config.Subscription{}).Error
		_ = config.DB.Where("id = ?", userID).Delete(&config.User{}).Error
	})
	return userID
}
