package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type sessionBillingPolicyWire struct {
	Mode                         string `json:"mode"`
	ProcessingUnitLimitsEnforced bool   `json:"processing_unit_limits_enforced"`
	PurchasesEnabled             bool   `json:"purchases_enabled"`
}

type sessionResponseWire struct {
	Type          string                   `json:"type"`
	BillingPolicy sessionBillingPolicyWire `json:"billing_policy"`
	Subscription  *SessionSubscription     `json:"subscription"`
}

func sessionTestApp(ident identity.Identity) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, ident)
		return c.Next()
	})
	app.Get("/api/auth/session", NewController(NewService()).Session)
	return app
}

func getSessionResponse(t *testing.T, app *fiber.App) (*http.Response, sessionResponseWire) {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil))
	require.NoError(t, err)
	var decoded sessionResponseWire
	require.NoError(t, json.NewDecoder(response.Body).Decode(&decoded))
	return response, decoded
}

func TestSessionGuestBillingPolicyReflectsCurrentMode(t *testing.T) {
	app := sessionTestApp(identity.Identity{ID: "guest-session-test", Type: identity.TypeGuest, Trust: 1})
	for _, test := range []struct {
		mode string
		want sessionBillingPolicyWire
	}{
		{mode: "normal", want: sessionBillingPolicyWire{Mode: "normal", ProcessingUnitLimitsEnforced: true, PurchasesEnabled: true}},
		{mode: "free", want: sessionBillingPolicyWire{Mode: "free", ProcessingUnitLimitsEnforced: false, PurchasesEnabled: false}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("BILLING_MODE", test.mode)
			response, got := getSessionResponse(t, app)
			defer response.Body.Close()

			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "guest", got.Type)
			require.Equal(t, test.want, got.BillingPolicy)
			require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
		})
	}
}

func TestSessionGuestBillingPolicyChangesBetweenRequests(t *testing.T) {
	app := sessionTestApp(identity.Identity{ID: "guest-mode-change-test", Type: identity.TypeGuest, Trust: 1})
	t.Setenv("BILLING_MODE", "normal")
	first, firstSession := getSessionResponse(t, app)
	require.Equal(t, http.StatusOK, first.StatusCode)
	first.Body.Close()

	t.Setenv("BILLING_MODE", "free")
	second, secondSession := getSessionResponse(t, app)
	defer second.Body.Close()
	require.Equal(t, http.StatusOK, second.StatusCode)
	require.Equal(t, "normal", firstSession.BillingPolicy.Mode)
	require.Equal(t, "free", secondSession.BillingPolicy.Mode)
}

func TestSessionRejectsInvalidBillingMode(t *testing.T) {
	t.Setenv("BILLING_MODE", "unlimited")
	app := sessionTestApp(identity.Identity{ID: "guest-invalid-mode-test", Type: identity.TypeGuest, Trust: 1})
	response, _ := getSessionResponse(t, app)
	defer response.Body.Close()
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}

func TestSessionAuthenticatedPolicyDoesNotRewriteStoredSubscription(t *testing.T) {
	db := setupGIM8SessionTestDB(t)
	now := time.Now().UTC()
	userID := uuid.NewString()
	user := config.User{ID: userID, Email: userID + "@example.test", Role: "user", Status: "active", EmailVerified: true, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&user).Error)
	subscriptionID := uuid.NewString()
	subscription := config.Subscription{
		ID: subscriptionID, UserID: userID, PaddleCustomerID: "customer-" + subscriptionID,
		PaddleSubscriptionID: "subscription-" + subscriptionID, Tier: "pro", Status: "active",
		BillingInterval: "yearly", CustomCredits: 120, UsedUnits3h: 3, UsedUnitsDaily: 5, UsedUnitsMonthly: 8,
		CurrentPeriodEnd: now.AddDate(0, 1, 0), Window3HResetAt: now.Add(time.Hour),
		WindowDailyResetAt: now.Add(24 * time.Hour), WindowMonthlyResetAt: now.AddDate(0, 1, 0),
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&subscription).Error)
	t.Cleanup(func() {
		_ = db.Where("user_id = ?", userID).Delete(&config.Subscription{}).Error
		_ = db.Where("id = ?", userID).Delete(&config.User{}).Error
	})
	app := sessionTestApp(identity.Identity{ID: userID, Type: identity.TypeUser, Role: "user"})

	for _, test := range []struct {
		mode string
		want sessionBillingPolicyWire
	}{
		{mode: "normal", want: sessionBillingPolicyWire{Mode: "normal", ProcessingUnitLimitsEnforced: true, PurchasesEnabled: true}},
		{mode: "free", want: sessionBillingPolicyWire{Mode: "free", ProcessingUnitLimitsEnforced: false, PurchasesEnabled: false}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("BILLING_MODE", test.mode)
			response, got := getSessionResponse(t, app)
			defer response.Body.Close()

			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "user", got.Type)
			require.Equal(t, test.want, got.BillingPolicy)
			require.NotNil(t, got.Subscription)
			require.Equal(t, "pro", got.Subscription.Tier)
			require.Equal(t, "active", got.Subscription.Status)
			require.Equal(t, 120, got.Subscription.CustomCredits)
		})
	}

	var persisted config.Subscription
	require.NoError(t, db.First(&persisted, "user_id = ?", userID).Error)
	require.Equal(t, "pro", persisted.Tier)
	require.Equal(t, "active", persisted.Status)
	require.Equal(t, 120, persisted.CustomCredits)
	require.Equal(t, 3, persisted.UsedUnits3h)
	require.Equal(t, 5, persisted.UsedUnitsDaily)
	require.Equal(t, 8, persisted.UsedUnitsMonthly)
}

func setupGIM8SessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("GIM6_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("GIM6_TEST_DATABASE_URL is unset; isolated local PostgreSQL session assertions were not run")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err, "GIM6_TEST_DATABASE_URL must be a PostgreSQL URL")
	require.True(t, parsed.Scheme == "postgres" || parsed.Scheme == "postgresql", "GIM6_TEST_DATABASE_URL must use postgres:// or postgresql://")
	host := strings.ToLower(parsed.Hostname())
	require.True(t, host == "localhost" || host == "127.0.0.1" || host == "::1", "GIM6_TEST_DATABASE_URL must point to local PostgreSQL")
	for _, name := range []string{"host", "hostaddr", "service"} {
		require.Empty(t, parsed.Query().Get(name), "GIM6_TEST_DATABASE_URL must not override its local host via %s", name)
	}
	databaseName, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(databaseName, "test_") || strings.HasSuffix(databaseName, "_test"), "GIM6_TEST_DATABASE_URL database name must start with test_ or end with _test")

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skipf("explicitly configured isolated local PostgreSQL is unavailable: %v", err)
	}
	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}))
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() {
		config.DB = previousDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
