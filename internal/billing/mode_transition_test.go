package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLegacyAsyncReservationKindResolvesGuestStoreAfterModeSwitch(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	_ = db
	store, client := installGIM7GuestQuota(t)
	ctx := context.Background()
	guestID := "gim7-guest-" + uuid.NewString()
	tool := Tool{Name: "gim7-transition", BaseUnits: 1, Estimate: EstimateNone()}

	committed, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/gim7/commit", "task-commit")
	require.NoError(t, err)
	require.NotEmpty(t, committed.ID)
	require.Equal(t, ReservationKindGuest, committed.Kind)

	released, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/gim7/release", "task-release")
	require.NoError(t, err)
	require.NotEmpty(t, released.ID)

	t.Setenv("BILLING_MODE", "free")
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, ""))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, ""))
	// Replays after the reservation key is deleted remain harmless.
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, ""))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, ""))

	state, err := client.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1", state["used_3h"])
	require.Equal(t, "0", state["pending_3h"])
	require.Equal(t, "0", state["pending_day"])
	require.Equal(t, "0", state["pending_month"])
	for _, id := range []string{committed.ID, released.ID} {
		exists, existsErr := client.Exists(ctx, store.resKey(id)).Result()
		require.NoError(t, existsErr)
		require.Zero(t, exists)
	}
}

func TestLegacyAsyncReservationKindResolvesDatabaseStoreAfterModeSwitch(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	_, _ = installGIM7GuestQuota(t)
	ctx := context.Background()

	committed, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/commit", "task-commit")
	require.NoError(t, err)
	released, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/release", "task-release")
	require.NoError(t, err)
	require.NotEmpty(t, committed.ID)
	require.NotEmpty(t, released.ID)

	t.Setenv("BILLING_MODE", "free")
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, ""))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, ""))
	require.NoError(t, Default.CommitAsync(ctx, committed.ID, ""))
	require.NoError(t, Default.ReleaseAsync(ctx, released.ID, ""))

	var committedRow, releasedRow config.BillingReservation
	require.NoError(t, db.Where("id = ?", committed.ID).First(&committedRow).Error)
	require.NoError(t, db.Where("id = ?", released.ID).First(&releasedRow).Error)
	require.Equal(t, "committed", committedRow.Status)
	require.Equal(t, "released", releasedRow.Status)
	var sub config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&sub).Error)
	require.Equal(t, committedRow.PlanUnits, sub.UsedUnits3h)
	require.Equal(t, committedRow.PlanUnits, sub.UsedUnitsDaily)
	require.Equal(t, committedRow.PlanUnits, sub.UsedUnitsMonthly)
	require.Equal(t, 20-committedRow.CreditUnits, sub.CustomCredits)
}

func TestFreeAsyncAllocationsRemainUnbilledAfterSwitchToNormal(t *testing.T) {
	previousDB := config.DB
	previousQuota := GuestQuota
	config.DB = nil
	GuestQuota = nil
	t.Cleanup(func() {
		config.DB = previousDB
		GuestQuota = previousQuota
	})
	t.Setenv("BILLING_MODE", "free")
	ctx := context.Background()
	tool := Tool{Name: "gim7-free-transition", BaseUnits: 1, Estimate: EstimateNone()}

	account, err := Default.ReserveAsync(ctx, "gim7-account", "", tool, 0, 0, "/gim7/free", "task-account")
	require.NoError(t, err)
	guest, err := Default.ReserveAsync(ctx, "gim7-guest", "gim7-guest", tool, 0, 0, "/gim7/free", "task-guest")
	require.NoError(t, err)
	require.NotNil(t, account)
	require.NotNil(t, guest)
	require.Empty(t, account.ID)
	require.Empty(t, guest.ID)

	t.Setenv("BILLING_MODE", "normal")
	require.NoError(t, Default.CommitAsync(ctx, account.ID, account.Kind))
	require.NoError(t, Default.ReleaseAsync(ctx, account.ID, account.Kind))
	require.NoError(t, Default.CommitAsync(ctx, guest.ID, guest.Kind))
	require.NoError(t, Default.ReleaseAsync(ctx, guest.ID, guest.Kind))
}

func TestGuestCommitReleaseRaceAcrossModeTransitionSettlesAtMostOnce(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	store, client := installGIM7GuestQuota(t)
	ctx := context.Background()
	guestID := "gim7-race-" + uuid.NewString()
	reservation, err := Default.ReserveAsync(ctx, guestID, guestID, Tool{Name: "gim7-race", BaseUnits: 1, Estimate: EstimateNone()}, 0, 0, "/gim7/race", "task-race")
	require.NoError(t, err)

	t.Setenv("BILLING_MODE", "free")
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- Default.CommitAsync(ctx, reservation.ID, reservation.Kind)
	}()
	go func() {
		<-start
		results <- Default.ReleaseAsync(ctx, reservation.ID, reservation.Kind)
	}()
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)

	state, err := client.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"])
	used := state["used_3h"]
	require.True(t, used == "0" || used == "1", "commit/release race charged more than one allocation: %#v", state)
	for _, key := range []string{store.resKey(reservation.ID)} {
		exists, existsErr := client.Exists(ctx, key).Result()
		require.NoError(t, existsErr)
		require.Zero(t, exists)
	}
}

func TestLegacyAsyncReservationKindRejectsAmbiguousStoreOwnership(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	store, client := installGIM7GuestQuota(t)
	ctx := context.Background()
	guestID := "gim7-ambiguous-" + uuid.NewString()
	guestReservation, err := store.ReserveAsync(ctx, guestID, Tool{Name: "gim7-ambiguous", BaseUnits: 1, Estimate: EstimateNone()}, 0, 0, "/gim7/ambiguous")
	require.NoError(t, err)
	now := time.Now()
	databaseReservation := config.BillingReservation{
		ID: guestReservation.ID, UserID: userID, ToolName: "gim7-ambiguous", Units: 1,
		PlanUnits: 1, Status: "reserved", ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&databaseReservation).Error)

	t.Setenv("BILLING_MODE", "free")
	err = Default.CommitAsync(ctx, guestReservation.ID, "")
	require.Error(t, err, "blank-kind IDs present in both stores must not be settled by guesswork")
	var stored config.BillingReservation
	require.NoError(t, db.Where("id = ?", guestReservation.ID).First(&stored).Error)
	require.Equal(t, "reserved", stored.Status)
	exists, err := client.Exists(ctx, store.resKey(guestReservation.ID)).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, exists)
}

func TestLegacyAsyncReservationKindDoesNotHideDatabaseLookupFailure(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	store, client := installGIM7GuestQuota(t)
	guestID := "gim7-db-error-" + uuid.NewString()
	reservation, err := store.ReserveAsync(context.Background(), guestID, Tool{Name: "gim7-db-error", BaseUnits: 1, Estimate: EstimateNone()}, 0, 0, "/gim7/db-error")
	require.NoError(t, err)
	require.NotEmpty(t, reservation.ID)
	previousDB := config.DB
	brokenDB, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=gim7_test dbname=test_gim7 sslmode=disable connect_timeout=1"), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	config.DB = brokenDB
	t.Cleanup(func() { config.DB = previousDB })

	t.Setenv("BILLING_MODE", "free")
	err = Default.CommitAsync(context.Background(), reservation.ID, "")
	require.ErrorContains(t, err, "check database reservation ownership")
	require.False(t, errors.Is(err, gorm.ErrRecordNotFound))
	exists, existsErr := client.Exists(context.Background(), store.resKey(reservation.ID)).Result()
	require.NoError(t, existsErr)
	require.EqualValues(t, 1, exists, "a Redis match must not be selected after the DB ownership lookup fails")
}

func TestLegacyAsyncReservationKindDoesNotHideRedisLookupFailure(t *testing.T) {
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	t.Setenv("BILLING_MODE", "normal")
	reservation, err := Default.ReserveAsync(context.Background(), userID, "", ConvertURLToPDF, 0, 0, "/gim7/redis-error", "task-redis-error")
	require.NoError(t, err)

	brokenClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: 0})
	previousQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(brokenClient, time.Hour)
	t.Cleanup(func() {
		GuestQuota = previousQuota
		_ = brokenClient.Close()
	})
	t.Setenv("BILLING_MODE", "free")
	err = Default.CommitAsync(context.Background(), reservation.ID, "")
	require.ErrorContains(t, err, "check guest reservation ownership")
	var stored config.BillingReservation
	require.NoError(t, db.Where("id = ?", reservation.ID).First(&stored).Error)
	require.Equal(t, "reserved", stored.Status, "do not select the database when the Redis ownership check failed")
}

func TestLegacyAsyncReservationKindUsesNoopForNeitherStoreOnlyAfterBothLookupsSucceed(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	_ = setupGIM6IsolatedTestDB(t)
	_, _ = installGIM7GuestQuota(t)
	// Both backing stores are reachable and report no reservation. This is the
	// existing guest finalizer's absent/expired reservation no-op contract.
	absentID := uuid.NewString()
	require.NoError(t, Default.CommitAsync(context.Background(), absentID, ""))
	require.NoError(t, Default.ReleaseAsync(context.Background(), absentID, ""))
}

func TestLegacyAsyncReservationKindDoesNotTreatUnavailableGuestStoreAsAbsent(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	_ = setupGIM6IsolatedTestDB(t)
	previousQuota := GuestQuota
	GuestQuota = nil
	t.Cleanup(func() {
		GuestQuota = previousQuota
	})

	err := Default.ReleaseAsync(context.Background(), uuid.NewString(), "")
	require.ErrorIs(t, err, ErrGuestQuotaStoreUnavailable)
	require.False(t, errors.Is(err, redis.Nil))
}

func installGIM7GuestQuota(t *testing.T) (*GuestQuotaStore, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() {
		GuestQuota = previousQuota
		_ = client.Close()
	})
	return GuestQuota, client
}
