package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/models"
)

func cancellationRouteApp(t *testing.T, registry *TaskRegistry) *fiber.App {
	t.Helper()
	oldRegistry := Registry
	Registry = registry
	t.Cleanup(func() { Registry = oldRegistry })

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		c.Locals(identity.LocalIdentityType, c.Get("X-Test-Identity-Type"))
		return c.Next()
	})
	RegisterRoutes(app)
	return app
}

func cancellationRequest(app *fiber.App, taskID, requesterID, requesterType, token string) (*http.Response, error) {
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/tasks/%s", taskID), nil)
	req.Header.Set("X-Test-Identity", requesterID)
	req.Header.Set("X-Test-Identity-Type", requesterType)
	if token != "" {
		req.Header.Set("X-Download-Token", token)
	}
	return app.Test(req, 5000)
}

func withCancellationBillingSpy(t *testing.T) func() int {
	t.Helper()
	previous := CancelTaskBillingHandlerWithKind
	var mu sync.Mutex
	calls := 0
	CancelTaskBillingHandlerWithKind = func(string, string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	}
	t.Cleanup(func() { CancelTaskBillingHandlerWithKind = previous })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func createPublicCancellationLedger(t *testing.T, dbExec *gorm.DB) {
	t.Helper()
	require.NoError(t, dbExec.Exec("CREATE TABLE IF NOT EXISTS public.account_storage_cleanups (user_id uuid PRIMARY KEY)").Error)
	t.Cleanup(func() { _ = dbExec.Exec("DROP TABLE IF EXISTS public.account_storage_cleanups").Error })
}

func TestActiveAccountCanCancelRedisOnlyTaskWithLedgerLock(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&owner).Error)
	const taskID = "active-account-redis-only-task"
	const capability = "active-account-redis-only-capability"
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner.ID, capability, "active-account-reservation")
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, owner.ID, string(identity.TypeUser), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, 1, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", projected.Status)
}

func TestCancelDeletedAccountTaskRejectsPreviouslyIssuedCapability(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	owner := uuid.NewString()
	const taskID = "cancel-deleted-account"
	const capability = "issued-before-account-deletion"
	row := models.TaskCompletion{ID: taskID, OwnerIdentity: owner, DownloadToken: capability, ReservationID: "reservation-to-keep", ReservationKind: "credits", State: "active", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&row).Error)
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner, capability, row.ReservationID, row.ReservationKind)
	require.NoError(t, err)
	require.NoError(t, db.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", owner).Error)

	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, owner, string(identity.TypeUser), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	var body map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.JSONEq(t, `"FORBIDDEN"`, string(body["code"]))
	require.Zero(t, getBillingCalls(), "a revoked capability must not reach billing cancellation")

	var after models.TaskCompletion
	require.NoError(t, db.First(&after, "id = ?", taskID).Error)
	require.Equal(t, "active", after.State)
	require.Equal(t, "reservation-to-keep", after.ReservationID)
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}

func TestCancelAccountTaskFailsClosedWhenDatabaseIsUnavailable(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })

	owner := uuid.NewString()
	const taskID = "cancel-account-no-database"
	const capability = "old-account-capability"
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner, capability, "reservation-must-not-cancel")
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, owner, string(identity.TypeUser), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Zero(t, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}

func TestCancelRedisOnlyAccountTaskRequiresDurableAuthorization(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := uuid.NewString()
	const taskID = "cancel-account-without-durable-row"
	const capability = "cached-account-capability"
	require.NoError(t, db.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", owner).Error)
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner, capability, "reservation-must-not-cancel")
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, owner, string(identity.TypeUser), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.Zero(t, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}

func TestCancelRedisOnlyTaskCannotUseCapabilityOfSoftDeletedAccountWithoutLedgerMarker(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Model(&owner).Update("status", "deleted").Error)
	require.NoError(t, db.Delete(&owner).Error)
	const taskID = "cancel-historically-deleted-account-task"
	const capability = "capability-from-soft-deleted-account"
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner.ID, capability, "reservation-must-not-cancel")
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, uuid.NewString(), string(identity.TypeGuest), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.Zero(t, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}

func TestCancelDurableTaskCannotUseCapabilityOfSoftDeletedAccountWithoutLedgerMarker(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Model(&owner).Update("status", "deleted").Error)
	require.NoError(t, db.Delete(&owner).Error)
	const taskID = "cancel-historically-deleted-durable-task"
	const capability = "durable-capability-from-soft-deleted-account"
	row := models.TaskCompletion{ID: taskID, OwnerIdentity: owner.ID, DownloadToken: capability, ReservationID: "reservation-must-not-cancel", State: "active", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&row).Error)
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner.ID, capability, row.ReservationID)
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, uuid.NewString(), string(identity.TypeGuest), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.Zero(t, getBillingCalls())
	var after models.TaskCompletion
	require.NoError(t, db.First(&after, "id = ?", taskID).Error)
	require.Equal(t, "active", after.State)
	require.Equal(t, capability, after.DownloadToken)
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}

func TestCancelAccountTaskFailsClosedWhenDeletionLedgerQueryFails(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	_ = db.Exec("DROP TABLE IF EXISTS public.account_storage_cleanups").Error
	owner := uuid.NewString()
	const taskID = "cancel-account-ledger-unavailable"
	const capability = "ledger-outage-capability"
	row := models.TaskCompletion{ID: taskID, OwnerIdentity: owner, DownloadToken: capability, ReservationID: "keep-reservation", State: "active", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&row).Error)
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner, capability, row.ReservationID)
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, owner, string(identity.TypeUser), capability)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Zero(t, getBillingCalls())
	var after models.TaskCompletion
	require.NoError(t, db.First(&after, "id = ?", taskID).Error)
	require.Equal(t, "active", after.State)
}

func TestCancelUUIDGuestTaskWithoutDatabase(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })

	guestID := uuid.NewString()
	const taskID = "cancel-uuid-guest-without-database"
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", guestID, "guest-cancel-token", "guest-reservation", "guest")
	require.NoError(t, err)
	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	response, err := cancellationRequest(app, taskID, guestID, string(identity.TypeGuest), "")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.JSONEq(t, `"CANCELLED"`, string(body["status"]))
	require.Equal(t, 1, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", projected.Status)
}

func TestConcurrentAccountDeletionSerializesDurableCancellationByAccountFirst(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&owner).Error)
	const taskID = "durable-cancel-vs-account-deletion-user-lock"
	const capability = "durable-account-lock-capability"
	row := models.TaskCompletion{ID: taskID, OwnerIdentity: owner.ID, DownloadToken: capability, ReservationID: "account-lock-reservation", State: "active", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&row).Error)
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner.ID, capability, row.ReservationID)
	require.NoError(t, err)

	deleteTx := db.Begin()
	require.NoError(t, deleteTx.Error)
	var lockedOwner config.User
	require.NoError(t, deleteTx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedOwner, "id = ?", owner.ID).Error)
	require.NoError(t, deleteTx.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", owner.ID).Error)
	require.NoError(t, deleteTx.Model(&lockedOwner).Updates(map[string]any{"status": "deleted", "session_version": gorm.Expr("session_version + 1"), "updated_at": time.Now()}).Error)
	require.NoError(t, deleteTx.Delete(&lockedOwner).Error)
	require.NoError(t, deleteTx.Model(&models.TaskCompletion{}).Where("owner_identity = ?", owner.ID).Update("download_token", "").Error)
	defer deleteTx.Rollback()

	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	requestDone := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, requestErr := cancellationRequest(app, taskID, owner.ID, string(identity.TypeUser), capability)
		requestDone <- struct {
			response *http.Response
			err      error
		}{response: response, err: requestErr}
	}()

	// Durable cancellation must wait on the account row before it locks the
	// task row, matching the production account-deletion lock order.
	require.Eventually(t, func() bool {
		var waiting int64
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%users%'`).Scan(&waiting).Error == nil && waiting > 0
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, deleteTx.Commit().Error)

	select {
	case result := <-requestDone:
		require.NoError(t, result.err)
		defer result.response.Body.Close()
		require.Equal(t, http.StatusForbidden, result.response.StatusCode)
	case <-time.After(6 * time.Second):
		t.Fatal("durable task cancellation did not finish after account deletion committed")
	}
	require.Zero(t, getBillingCalls())
	var after models.TaskCompletion
	require.NoError(t, db.First(&after, "id = ?", taskID).Error)
	require.Equal(t, "active", after.State)
	require.Empty(t, after.DownloadToken)
}

func TestConcurrentAccountDeletionSerializesRedisOnlyCancellation(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	createPublicCancellationLedger(t, db)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	owner := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&owner).Error)
	const taskID = "redis-only-cancel-vs-account-deletion"
	const capability = "redis-only-concurrent-capability"
	_, err := registry.SetWithDownloadToken(taskID, "PROCESSING", 20, "", "", owner.ID, capability, "concurrent-account-reservation")
	require.NoError(t, err)

	deleteTx := db.Begin()
	require.NoError(t, deleteTx.Error)
	var lockedOwner config.User
	require.NoError(t, deleteTx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedOwner, "id = ?", owner.ID).Error)
	require.NoError(t, deleteTx.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", owner.ID).Error)
	require.NoError(t, deleteTx.Model(&lockedOwner).Updates(map[string]any{"status": "deleted", "session_version": gorm.Expr("session_version + 1"), "updated_at": time.Now()}).Error)
	require.NoError(t, deleteTx.Delete(&lockedOwner).Error)
	defer deleteTx.Rollback()

	getBillingCalls := withCancellationBillingSpy(t)
	app := cancellationRouteApp(t, registry)
	requestDone := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, requestErr := cancellationRequest(app, taskID, owner.ID, string(identity.TypeUser), capability)
		requestDone <- struct {
			response *http.Response
			err      error
		}{response: response, err: requestErr}
	}()

	// The cancellation must wait on the same account row held by deletion,
	// because this task has no durable TaskCompletion row to provide a lock.
	require.Eventually(t, func() bool {
		var waiting int64
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%users%'`).Scan(&waiting).Error == nil && waiting > 0
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, deleteTx.Commit().Error)

	select {
	case result := <-requestDone:
		require.NoError(t, result.err)
		defer result.response.Body.Close()
		require.Equal(t, http.StatusForbidden, result.response.StatusCode)
	case <-time.After(6 * time.Second):
		t.Fatal("Redis-only task cancellation did not finish after account deletion committed")
	}
	require.Zero(t, getBillingCalls())
	projected, err := registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "PROCESSING", projected.Status)
}
