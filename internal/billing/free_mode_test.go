package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	recovermw "github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFreeModeAuthenticatedAllocationShortCircuitsDatabase(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })

	var syncReservation *config.BillingReservation
	var syncErr error
	require.NotPanics(t, func() {
		syncReservation, syncErr = Default.ReserveWithTaskID(
			"account-free", ConvertURLToPDF, 3, 2, "/api/conversion/url-to-pdf", "",
		)
	}, "free allocation must not dereference the billing database")
	require.NoError(t, syncErr)
	require.NotNil(t, syncReservation)
	require.Empty(t, syncReservation.ID)
	require.Zero(t, syncReservation.Units)
	require.Zero(t, syncReservation.PlanUnits)
	require.Zero(t, syncReservation.CreditUnits)
	require.Equal(t, "account-free", syncReservation.UserID)
	require.Equal(t, ConvertURLToPDF.Name, syncReservation.ToolName)
	require.Equal(t, 3, syncReservation.PagesCount)
	require.Equal(t, 2, syncReservation.ImagesCount)

	var asyncReservation *AsyncReservation
	var asyncErr error
	require.NotPanics(t, func() {
		asyncReservation, asyncErr = Default.ReserveAsync(
			context.Background(), "account-free", "", ConvertURLToPDF, 3, 2,
			"/api/conversion/url-to-pdf-async", "task-free",
		)
	}, "free async account allocation must not dereference the billing database")
	require.NoError(t, asyncErr)
	require.NotNil(t, asyncReservation)
	require.Empty(t, asyncReservation.ID)
	require.Equal(t, ReservationKindDatabase, asyncReservation.Kind)
}

func TestFreeModeGuestAsyncAllocationDoesNotRequireGuestQuotaStore(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousQuota := GuestQuota
	GuestQuota = nil
	t.Cleanup(func() { GuestQuota = previousQuota })

	reservation, err := Default.ReserveAsync(
		context.Background(), "guest-owner", "guest-quota-key", ExtractTextPDF, 4, 0,
		"/api/ocr/extract-text-async", "task-free-guest",
	)
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Empty(t, reservation.ID)
	require.Equal(t, ReservationKindGuest, reservation.Kind)
	require.NoError(t, Default.CommitAsync(context.Background(), reservation.ID, reservation.Kind))
	require.NoError(t, Default.ReleaseAsync(context.Background(), reservation.ID, reservation.Kind))
}

func TestFreeModeGuestStoreAllocationSkipsExhaustedQuotaAndWrites(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewGuestQuotaStore(client, time.Hour)
	ctx := context.Background()
	guestID := "free-exhausted-guest"
	now := time.Now()
	state := guestState{
		Used3H: 4, UsedDay: 10, UsedMonth: 30,
		Pending3H: 1, PendingDay: 2, PendingMonth: 3,
		Window3HResetAt:      now.Add(time.Hour),
		WindowDailyResetAt:   now.Add(24 * time.Hour),
		WindowMonthlyResetAt: now.AddDate(0, 1, 0),
	}
	require.NoError(t, client.HSet(ctx, store.stateKey(guestID), state.toMap()...).Err())
	before, err := client.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)

	syncReservation, err := store.Reserve(ctx, guestID, ExtractTextPDF, 4, 0, "/sync")
	require.NoError(t, err)
	require.NotNil(t, syncReservation)
	require.Empty(t, syncReservation.ID)
	require.Zero(t, syncReservation.Units)

	asyncReservation, err := store.ReserveAsync(ctx, guestID, ExtractTextPDF, 4, 0, "/async")
	require.NoError(t, err)
	require.NotNil(t, asyncReservation)
	require.Empty(t, asyncReservation.ID)
	require.Zero(t, asyncReservation.Units)

	after, err := client.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, before, after, "free allocations must leave existing billing quota state unchanged")
	reservationKeys, err := client.Keys(ctx, "platen:guestquota:res:*").Result()
	require.NoError(t, err)
	require.Empty(t, reservationKeys, "free allocations must not create guest reservation keys")
}

func TestFreeModeGuestMiddlewareRunsWithoutBillingStoreAndSetsLocals(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousQuota := GuestQuota
	GuestQuota = nil
	t.Cleanup(func() { GuestQuota = previousQuota })

	var reservationID string
	var consumedViaCredit bool
	var hasConsumedViaCredit bool
	var billingKind string
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalIdentityIDKey, "middleware-free-guest")
		return c.Next()
	})
	app.Post("/operation", UseGuestOnly(ConvertURLToPDF), func(c *fiber.Ctx) error {
		reservationID, _ = c.Locals("billing_reservation_id").(string)
		consumedViaCredit, hasConsumedViaCredit = c.Locals("consumed_via_credit").(bool)
		billingKind, _ = c.Locals("billing_kind").(string)
		return c.SendStatus(fiber.StatusAccepted)
	})

	response, err := app.Test(httptest.NewRequest("POST", "/operation", nil))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusAccepted, response.StatusCode)
	require.Empty(t, reservationID)
	require.True(t, hasConsumedViaCredit)
	require.False(t, consumedViaCredit)
	require.Equal(t, "guest", billingKind)
}

func TestFreeModeAccountMiddlewareSetsCompatibleLocals(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })

	var reservationID string
	var consumedViaCredit bool
	var hasConsumedViaCredit bool
	app := fiber.New()
	app.Use(recovermw.New())
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeUser))
		c.Locals(identity.LocalIdentityIDKey, "account-free-middleware")
		return c.Next()
	})
	app.Post("/operation", Use(ConvertURLToPDF), func(c *fiber.Ctx) error {
		reservationID, _ = c.Locals("billing_reservation_id").(string)
		consumedViaCredit, hasConsumedViaCredit = c.Locals("consumed_via_credit").(bool)
		return c.SendStatus(fiber.StatusAccepted)
	})

	var response *http.Response
	var err error
	require.NotPanics(t, func() {
		response, err = app.Test(httptest.NewRequest("POST", "/operation", nil))
	}, "free middleware must not access the billing database")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusAccepted, response.StatusCode)
	require.Empty(t, reservationID)
	require.True(t, hasConsumedViaCredit)
	require.False(t, consumedViaCredit)
}

func TestEmptyGuestReservationFinalizationDoesNotRequireRedis(t *testing.T) {
	var store *GuestQuotaStore
	ctx := context.Background()

	require.NotPanics(t, func() {
		require.NoError(t, store.Commit(ctx, ""))
		require.NoError(t, store.Release(ctx, " "))
	}, "empty guest reservation IDs must be no-ops before Redis access")

	previousQuota := GuestQuota
	GuestQuota = nil
	t.Cleanup(func() { GuestQuota = previousQuota })
	require.NoError(t, Default.CommitAsync(ctx, "", ReservationKindGuest))
	require.NoError(t, Default.ReleaseAsync(ctx, "", ReservationKindGuest))
}

func TestRealGuestReservationsFinalizeAfterModeChangesToFree(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	previousQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() { GuestQuota = previousQuota })

	ctx := context.Background()
	guestID := "mode-transition-guest"
	tool := Tool{Name: "transition_test", BaseUnits: 1, Estimate: EstimateNone()}
	committed, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/async", "task-commit")
	require.NoError(t, err)
	require.NotEmpty(t, committed.ID)
	released, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/async", "task-release")
	require.NoError(t, err)
	require.NotEmpty(t, released.ID)

	t.Setenv("BILLING_MODE", "free")
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, committed.Kind))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, released.Kind))

	state, err := client.HGetAll(ctx, GuestQuota.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1", state["used_3h"])
	require.Equal(t, "0", state["pending_3h"])
	require.Equal(t, "0", state["pending_day"])
	require.Equal(t, "0", state["pending_month"])
}

func TestNormalModeGuestSyncReservationStillUsesExistingQuota(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewGuestQuotaStore(client, time.Hour)
	ctx := context.Background()
	guestID := "normal-sync-guest"

	reservation, err := store.Reserve(ctx, guestID, ConvertURLToPDF, 0, 0, "/sync")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.NotEmpty(t, reservation.ID)
	require.Equal(t, 4, reservation.Units)
	require.NoError(t, store.Commit(ctx, reservation.ID))

	state, err := client.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "4", state["used_3h"])
	require.Equal(t, "0", state["pending_3h"])
	_, err = store.Reserve(ctx, guestID, ConvertURLToPDF, 0, 0, "/sync")
	require.Error(t, err)
	var billingErr *BillingError
	require.ErrorAs(t, err, &billingErr)
	require.Equal(t, string(ErrHourlyLimit), billingErr.Code)
}

func TestFreeModeAccountDatabaseAllocationDoesNotMutateExhaustedBillingState(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6ExhaustedAccount(t, db)

	var before config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&before).Error)
	var reservationsBefore, usageBefore int64
	require.NoError(t, db.Model(&config.BillingReservation{}).Where("user_id = ?", userID).Count(&reservationsBefore).Error)
	require.NoError(t, db.Model(&config.UsageLog{}).Where("user_id = ?", userID).Count(&usageBefore).Error)

	reservation, err := Default.ReserveWithTaskID(userID, ConvertURLToPDF, 0, 0, "/free", "task-free-db")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Empty(t, reservation.ID)
	require.Zero(t, reservation.PlanUnits)
	require.Zero(t, reservation.CreditUnits)
	require.Zero(t, reservation.Units)

	var after config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&after).Error)
	require.Equal(t, before.UsedUnits3h, after.UsedUnits3h)
	require.Equal(t, before.UsedUnitsDaily, after.UsedUnitsDaily)
	require.Equal(t, before.UsedUnitsMonthly, after.UsedUnitsMonthly)
	require.Equal(t, before.CustomCredits, after.CustomCredits)
	var reservationsAfter, usageAfter int64
	require.NoError(t, db.Model(&config.BillingReservation{}).Where("user_id = ?", userID).Count(&reservationsAfter).Error)
	require.NoError(t, db.Model(&config.UsageLog{}).Where("user_id = ?", userID).Count(&usageAfter).Error)
	require.Equal(t, reservationsBefore, reservationsAfter)
	require.Equal(t, usageBefore, usageAfter)
}

func TestNormalModeAccountAllocationStillRejectsExhaustedBillingState(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6ExhaustedAccount(t, db)

	_, err := Default.ReserveWithTaskID(userID, ConvertURLToPDF, 0, 0, "/normal", "task-normal-db")
	require.Error(t, err)
	var billingErr *BillingError
	require.ErrorAs(t, err, &billingErr)
	require.Equal(t, string(ErrHourlyLimit), billingErr.Code)
	var reservations int64
	require.NoError(t, db.Model(&config.BillingReservation{}).Where("user_id = ?", userID).Count(&reservations).Error)
	require.Zero(t, reservations)
}

func TestNormalModeAccountAllocationStillUsesPurchasedCredits(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 100, 10)

	reservation, err := Default.ReserveWithTaskID(userID, ConvertURLToPDF, 0, 0, "/normal-credit", "task-normal-credit")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.NotEmpty(t, reservation.ID)
	require.Equal(t, 4, reservation.Units)
	require.Zero(t, reservation.PlanUnits)
	require.Equal(t, 4, reservation.CreditUnits)
	require.NoError(t, Default.Release(reservation.ID))
}

func TestAuthenticatedReservationFinalizationUsesRealIDAfterModeChangesToFree(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	ctx := context.Background()

	committed, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/normal", "task-commit-mode-change")
	require.NoError(t, err)
	require.NotEmpty(t, committed.ID)
	require.Equal(t, ReservationKindDatabase, committed.Kind)
	released, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/normal", "task-release-mode-change")
	require.NoError(t, err)
	require.NotEmpty(t, released.ID)

	t.Setenv("BILLING_MODE", "free")
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, committed.Kind))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, released.Kind))

	var committedRow, releasedRow config.BillingReservation
	require.NoError(t, db.Where("id = ?", committed.ID).First(&committedRow).Error)
	require.NoError(t, db.Where("id = ?", released.ID).First(&releasedRow).Error)
	require.Equal(t, "committed", committedRow.Status)
	require.Equal(t, "released", releasedRow.Status)
	var subscription config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
	require.Equal(t, 4, subscription.UsedUnits3h)
	require.Equal(t, 4, subscription.UsedUnitsDaily)
	require.Equal(t, 4, subscription.UsedUnitsMonthly)
	require.Equal(t, 20, subscription.CustomCredits)
}

func setupGIM6IsolatedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("GIM6_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("GIM6_TEST_DATABASE_URL is unset; isolated PostgreSQL billing assertions were not run")
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
	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{}))
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previousDB })
	return db
}

func createGIM6ExhaustedAccount(t *testing.T, db *gorm.DB) string {
	return createGIM6AccountWithState(t, db, 100, 0)
}

func createGIM6AccountWithState(t *testing.T, db *gorm.DB, usedUnits, customCredits int) string {
	t.Helper()
	userID := uuid.NewString()
	now := time.Now()
	user := config.User{ID: userID, Email: userID + "@example.test", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&user).Error)
	subID := uuid.NewString()
	sub := config.Subscription{
		ID: subID, UserID: userID, PaddleCustomerID: "gim6_customer_" + subID,
		PaddleSubscriptionID: "gim6_subscription_" + subID, Tier: "free", Status: "active",
		CustomCredits: customCredits, UsedUnits3h: usedUnits, UsedUnitsDaily: usedUnits, UsedUnitsMonthly: usedUnits,
		Window3HResetAt: now.Add(time.Hour), WindowDailyResetAt: now.Add(24 * time.Hour),
		WindowMonthlyResetAt: now.AddDate(0, 1, 0), CurrentPeriodEnd: now.AddDate(1, 0, 0),
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&sub).Error)
	t.Cleanup(func() {
		_ = db.Where("user_id = ?", userID).Delete(&config.BillingReservation{}).Error
		_ = db.Where("user_id = ?", userID).Delete(&config.UsageLog{}).Error
		_ = db.Where("user_id = ?", userID).Delete(&config.Subscription{}).Error
		_ = db.Where("id = ?", userID).Delete(&config.User{}).Error
	})
	return userID
}
