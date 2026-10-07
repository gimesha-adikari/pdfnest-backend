package billing

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/tasks"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStaleTaskRecoveryReleasesStoredAndLegacyReservationKinds(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	t.Setenv("STUCK_TASK_TIMEOUT_SECONDS", "1")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	store, quotaClient := installGIM7GuestQuota(t)
	taskClient := installGIM7TaskRedis(t)
	Initialize(store)
	ctx := context.Background()
	guestID := "gim7-stale-" + uuid.NewString()
	tool := Tool{Name: "gim7-stale", BaseUnits: 1, Estimate: EstimateNone()}

	databaseReservation, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/stale", "task-db")
	require.NoError(t, err)
	legacyDatabaseReservation, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/stale-legacy", "task-db-legacy")
	require.NoError(t, err)
	guestReservation, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/gim7/stale", "task-guest")
	require.NoError(t, err)
	legacyGuestReservation, err := Default.ReserveAsync(ctx, guestID, guestID, tool, 0, 0, "/gim7/stale-legacy", "task-legacy")
	require.NoError(t, err)

	putGIM7StaleTask(t, taskClient, "gim7-stale-database", databaseReservation.ID, ReservationKindDatabase)
	putGIM7StaleTask(t, taskClient, "gim7-stale-legacy-database", legacyDatabaseReservation.ID, "")
	putGIM7StaleTask(t, taskClient, "gim7-stale-guest", guestReservation.ID, ReservationKindGuest)
	putGIM7StaleTask(t, taskClient, "gim7-stale-legacy-guest", legacyGuestReservation.ID, "")

	t.Setenv("BILLING_MODE", "free")
	for _, taskID := range []string{"gim7-stale-database", "gim7-stale-legacy-database", "gim7-stale-guest", "gim7-stale-legacy-guest"} {
		status, stale, _, getErr := tasks.Registry.GetWithTransition(taskID)
		require.NoError(t, getErr)
		require.True(t, stale, "expected stale transition for %s", taskID)
		require.Equal(t, "FAILED", status.Status)
		status, stale, _, getErr = tasks.Registry.GetWithTransition(taskID)
		require.NoError(t, getErr)
		require.False(t, stale, "terminal replay must not repeat stale transition for %s", taskID)
		_ = status
	}

	var storedDatabaseReservation config.BillingReservation
	require.NoError(t, db.Where("id = ?", databaseReservation.ID).First(&storedDatabaseReservation).Error)
	require.Equal(t, "released", storedDatabaseReservation.Status)
	var storedLegacyDatabaseReservation config.BillingReservation
	require.NoError(t, db.Where("id = ?", legacyDatabaseReservation.ID).First(&storedLegacyDatabaseReservation).Error)
	require.Equal(t, "released", storedLegacyDatabaseReservation.Status)
	var subscription config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
	require.Zero(t, subscription.UsedUnits3h)
	require.Zero(t, subscription.UsedUnitsDaily)
	require.Zero(t, subscription.UsedUnitsMonthly)
	require.Equal(t, 20, subscription.CustomCredits)
	state, err := quotaClient.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["used_3h"])
	require.Equal(t, "0", state["pending_3h"])
	require.Equal(t, "0", state["pending_day"])
	require.Equal(t, "0", state["pending_month"])
	for _, id := range []string{guestReservation.ID, legacyGuestReservation.ID} {
		exists, existsErr := quotaClient.Exists(ctx, store.resKey(id)).Result()
		require.NoError(t, existsErr)
		require.Zero(t, exists)
	}

	freeAllocation, err := Default.ReserveAsync(ctx, "gim7-free-account", "", tool, 0, 0, "/gim7/free-stale", "task-free")
	require.NoError(t, err)
	require.Empty(t, freeAllocation.ID)
	putGIM7StaleTask(t, taskClient, "gim7-stale-free", freeAllocation.ID, freeAllocation.Kind)
	previousDB, previousQuota := config.DB, GuestQuota
	config.DB = nil
	GuestQuota = nil
	t.Cleanup(func() {
		config.DB = previousDB
		GuestQuota = previousQuota
	})
	t.Setenv("BILLING_MODE", "normal")
	status, stale, reservationID, err := tasks.Registry.GetWithTransition("gim7-stale-free")
	require.NoError(t, err)
	require.True(t, stale)
	require.Empty(t, reservationID)
	require.Empty(t, status.ReservationID)
}

func TestTaskDownloadFinalizesKindsAcrossModeChangesAndEnforcesOwnership(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	userID := createGIM6AccountWithState(t, db, 0, 20)
	store, quotaClient := installGIM7GuestQuota(t)
	taskClient := installGIM7TaskRedis(t)
	Initialize(store)
	ctx := context.Background()
	guestID := "gim7-download-" + uuid.NewString()
	guestTool := Tool{Name: "gim7-download", BaseUnits: 1, Estimate: EstimateNone()}

	accountReservation, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/download", "task-account-download")
	require.NoError(t, err)
	legacyDatabaseReservation, err := Default.ReserveAsync(ctx, userID, "", ConvertURLToPDF, 0, 0, "/gim7/download-legacy", "task-account-legacy-download")
	require.NoError(t, err)
	guestReservation, err := Default.ReserveAsync(ctx, guestID, guestID, guestTool, 0, 0, "/gim7/download", "task-guest-download")
	require.NoError(t, err)
	legacyGuestReservation, err := Default.ReserveAsync(ctx, guestID, guestID, guestTool, 0, 0, "/gim7/download-legacy", "task-legacy-download")
	require.NoError(t, err)
	unauthorizedReservation, err := Default.ReserveAsync(ctx, "gim7-download-owner", "gim7-download-owner", guestTool, 0, 0, "/gim7/download-unauthorized", "task-unauthorized-download")
	require.NoError(t, err)

	resultPath := writeGIM7DownloadArtifact(t)
	putGIM7CompletedTask(t, taskClient, "gim7-download-account", userID, accountReservation.ID, string(accountReservation.Kind), resultPath)
	putGIM7CompletedTask(t, taskClient, "gim7-download-account-legacy", userID, legacyDatabaseReservation.ID, "", resultPath)
	putGIM7CompletedTask(t, taskClient, "gim7-download-guest", guestID, guestReservation.ID, string(guestReservation.Kind), resultPath)
	putGIM7CompletedTask(t, taskClient, "gim7-download-legacy", guestID, legacyGuestReservation.ID, "", resultPath)
	putGIM7CompletedTask(t, taskClient, "gim7-download-unauthorized", "guest-owner", unauthorizedReservation.ID, string(unauthorizedReservation.Kind), resultPath)

	t.Setenv("BILLING_MODE", "free")
	for _, taskID := range []string{"gim7-download-account", "gim7-download-account-legacy", "gim7-download-guest", "gim7-download-legacy"} {
		status := downloadGIM7Task(t, taskID, taskOwnerForGIM7(taskID, userID, guestID))
		require.Equal(t, fiber.StatusOK, status, "first download for %s", taskID)
		status = downloadGIM7Task(t, taskID, taskOwnerForGIM7(taskID, userID, guestID))
		require.Equal(t, fiber.StatusOK, status, "duplicate download for %s", taskID)
	}

	var accountRow config.BillingReservation
	require.NoError(t, db.Where("id = ?", accountReservation.ID).First(&accountRow).Error)
	require.Equal(t, "committed", accountRow.Status)
	var legacyAccountRow config.BillingReservation
	require.NoError(t, db.Where("id = ?", legacyDatabaseReservation.ID).First(&legacyAccountRow).Error)
	require.Equal(t, "committed", legacyAccountRow.Status)
	var sub config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&sub).Error)
	committedPlanUnits := accountRow.PlanUnits + legacyAccountRow.PlanUnits
	require.Equal(t, committedPlanUnits, sub.UsedUnits3h)
	require.Equal(t, committedPlanUnits, sub.UsedUnitsDaily)
	require.Equal(t, committedPlanUnits, sub.UsedUnitsMonthly)
	require.Equal(t, 20-accountRow.CreditUnits-legacyAccountRow.CreditUnits, sub.CustomCredits)
	state, err := quotaClient.HGetAll(ctx, store.stateKey(guestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"])
	require.Equal(t, "2", state["used_3h"])
	for _, id := range []string{guestReservation.ID, legacyGuestReservation.ID} {
		exists, existsErr := quotaClient.Exists(ctx, store.resKey(id)).Result()
		require.NoError(t, existsErr)
		require.Zero(t, exists)
	}

	freeGuest, err := Default.ReserveAsync(ctx, "gim7-free-download", "gim7-free-download", guestTool, 0, 0, "/gim7/free-download", "task-free-download")
	require.NoError(t, err)
	require.Empty(t, freeGuest.ID)
	putGIM7CompletedTask(t, taskClient, "gim7-download-free", "gim7-free-download", freeGuest.ID, string(freeGuest.Kind), resultPath)
	previousDB, previousQuota := config.DB, GuestQuota
	config.DB = nil
	GuestQuota = nil
	t.Setenv("BILLING_MODE", "normal")
	status := downloadGIM7Task(t, "gim7-download-free", "gim7-free-download")
	config.DB, GuestQuota = previousDB, previousQuota
	require.Equal(t, fiber.StatusOK, status, "empty-ID work must not consult either billing store after mode changes")

	status = downloadGIM7Task(t, "gim7-download-unauthorized", "different-owner")
	require.Equal(t, fiber.StatusForbidden, status)
	unauthorizedState, err := quotaClient.HGetAll(ctx, store.stateKey("gim7-download-owner")).Result()
	require.NoError(t, err)
	require.Equal(t, "1", unauthorizedState["pending_3h"], "unauthorized download must not finalize the reservation")
	unauthorizedExists, err := quotaClient.Exists(ctx, store.resKey(unauthorizedReservation.ID)).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, unauthorizedExists)
}

func installGIM7TaskRedis(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousRedis := config.Redis
	config.Redis = client
	t.Cleanup(func() {
		config.Redis = previousRedis
		_ = client.Close()
	})
	return client
}

func putGIM7StaleTask(t *testing.T, client *redis.Client, id, reservationID string, kind ReservationKind) {
	t.Helper()
	task := tasks.TaskStatus{
		ID: id, Status: "PROCESSING", Progress: 30, OwnerIdentity: "gim7-task-owner",
		ReservationID: reservationID, ReservationKind: string(kind), UpdatedAt: 1,
	}
	data, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, client.Set(context.Background(), tasks.TaskKeyPrefix+id, data, tasks.TaskTTL).Err())
}

func putGIM7CompletedTask(t *testing.T, client *redis.Client, id, owner, reservationID, kind, resultPath string) {
	t.Helper()
	task := tasks.TaskStatus{
		ID: id, Status: "COMPLETED", Progress: 100, OwnerIdentity: owner,
		ReservationID: reservationID, ReservationKind: kind, ResultURL: resultPath, UpdatedAt: time.Now().Unix(),
	}
	data, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, client.Set(context.Background(), tasks.TaskKeyPrefix+id, data, tasks.TaskTTL).Err())
}

func writeGIM7DownloadArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "result.txt")
	require.NoError(t, os.WriteFile(path, []byte("gim7 billing transition artifact"), 0o600))
	return path
}

func downloadGIM7Task(t *testing.T, taskID, requesterID string) int {
	t.Helper()
	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, requesterID)
		return tasks.HandleTaskDownload(c)
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func taskOwnerForGIM7(taskID, accountOwner, guestOwner string) string {
	if taskID == "gim7-download-account" || taskID == "gim7-download-account-legacy" {
		return accountOwner
	}
	return guestOwner
}
