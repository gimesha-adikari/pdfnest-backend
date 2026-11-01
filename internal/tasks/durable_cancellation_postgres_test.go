package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/models"
)

func setupTasksPostgresDB(t *testing.T) (*gorm.DB, *TaskRegistry, *miniredis.Miniredis) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	bootstrap, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}
	pool, err := bootstrap.DB()
	require.NoError(t, err)

	schema := "tasks_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, bootstrap.Exec("CREATE SCHEMA "+schema).Error)
	_ = pool.Close()

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&models.TaskCompletion{}))

	oldDB := config.DB
	config.DB = db

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	reg := &TaskRegistry{client: client}

	t.Cleanup(func() {
		config.DB = oldDB
		_ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = sqlDB.Close()
		_ = client.Close()
		srv.Close()
	})

	return db, reg, srv
}

func TestDurableCancellation_OwnerlessTask(t *testing.T) {
	db, reg, _ := setupTasksPostgresDB(t)

	const taskID = "durable-ownerless-task"
	const capToken = "cap-token-ownerless-123"
	const resID = "res-guest-1"

	row := models.TaskCompletion{
		ID:              taskID,
		OwnerIdentity:   "",
		DownloadToken:   capToken,
		ReservationID:   resID,
		ReservationKind: "guest",
		State:           "active",
		ExpiresAt:       time.Now().Add(time.Hour),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, db.Create(&row).Error)

	// 1. Unauthorized attempt: no capability token provided
	result, status, err := reg.CancelTask(taskID, "arbitrary-requester-ip")
	require.NoError(t, err)
	require.Equal(t, "UNAUTHORIZED", result)
	require.NotNil(t, status)
	require.Equal(t, "PROCESSING", status.Status)

	var checkRow models.TaskCompletion
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "active", checkRow.State, "unauthorized cancel must not modify state in database")
	require.Empty(t, checkRow.Error, "unauthorized cancel must not write error message")
	require.Equal(t, resID, checkRow.ReservationID, "reservation must remain intact")

	// 2. Unauthorized attempt: invalid capability token provided
	result, status, err = reg.CancelTask(taskID, "arbitrary-requester-ip", "wrong-capability")
	require.NoError(t, err)
	require.Equal(t, "UNAUTHORIZED", result)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "active", checkRow.State, "invalid capability token must not modify state in database")

	// 3. Authorized attempt: valid capability token provided
	result, status, err = reg.CancelTask(taskID, "arbitrary-requester-ip", capToken)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED_SUCCESS", result)
	require.NotNil(t, status)
	require.Equal(t, "CANCELLED", status.Status)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "cancelled", checkRow.State)
	require.Equal(t, "Task processing cancelled by user.", checkRow.Error)

	// 4. Subsequent cancel attempt: already terminal
	result, status, err = reg.CancelTask(taskID, "arbitrary-requester-ip", capToken)
	require.NoError(t, err)
	require.Equal(t, "TERMINAL_ALREADY", result)
}

func TestDurableCancellation_OwnedTask(t *testing.T) {
	db, reg, _ := setupTasksPostgresDB(t)

	const taskID = "durable-owned-task"
	const ownerAlice = "user:alice"
	const capToken = "cap-token-alice-456"

	row := models.TaskCompletion{
		ID:              taskID,
		OwnerIdentity:   ownerAlice,
		DownloadToken:   capToken,
		ReservationID:   "res-alice",
		ReservationKind: "subscription",
		State:           "active",
		ExpiresAt:       time.Now().Add(time.Hour),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, db.Create(&row).Error)

	// 1. Cross-owner unauthorized attempt: user Bob with no capability
	result, status, err := reg.CancelTask(taskID, "user:bob")
	require.NoError(t, err)
	require.Equal(t, "UNAUTHORIZED", result)

	var checkRow models.TaskCompletion
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "active", checkRow.State)

	// 2. Cross-owner unauthorized attempt: user Bob with invalid capability
	result, status, err = reg.CancelTask(taskID, "user:bob", "wrong-cap")
	require.NoError(t, err)
	require.Equal(t, "UNAUTHORIZED", result)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "active", checkRow.State)

	// 3. Authorized cancellation: valid owner identity
	result, status, err = reg.CancelTask(taskID, ownerAlice)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED_SUCCESS", result)
	require.NotNil(t, status)
	require.Equal(t, "CANCELLED", status.Status)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "cancelled", checkRow.State)

	// 4. Test owned task cancelled by valid capability from a guest or different identity
	const taskID2 = "durable-owned-task-2"
	const ownerCarol = "user:carol"
	const capTokenCarol = "cap-carol-789"

	row2 := models.TaskCompletion{
		ID:              taskID2,
		OwnerIdentity:   ownerCarol,
		DownloadToken:   capTokenCarol,
		ReservationID:   "res-carol",
		ReservationKind: "credits",
		State:           "active",
		ExpiresAt:       time.Now().Add(time.Hour),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, db.Create(&row2).Error)

	// Requester is a guest or anonymous caller with carol's capability token
	result, status, err = reg.CancelTask(taskID2, "guest-ip-123", capTokenCarol)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED_SUCCESS", result)
	require.NotNil(t, status)
	require.Equal(t, "CANCELLED", status.Status)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", taskID2).Error)
	require.Equal(t, "cancelled", checkRow.State)
}

func TestDurableCancellation_TerminalAndPublishingProtection(t *testing.T) {
	db, reg, _ := setupTasksPostgresDB(t)

	// 1. Completed task protection
	completedRow := models.TaskCompletion{
		ID:             "completed-task",
		OwnerIdentity:  "user:owner",
		DownloadToken:  "cap-comp",
		State:          "completed",
		ResultKey:      "outputs/tasks/completed-task/result.pdf",
		ResultURL:      "r2://outputs/tasks/completed-task/result.pdf",
		ArtifactSHA256: "abc123sha",
		ArtifactBytes:  1024,
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(&completedRow).Error)

	result, status, err := reg.CancelTask("completed-task", "user:owner", "cap-comp")
	require.NoError(t, err)
	require.Equal(t, "TERMINAL_ALREADY", result)
	require.Equal(t, "COMPLETED", status.Status)

	var checkRow models.TaskCompletion
	require.NoError(t, db.First(&checkRow, "id = ?", "completed-task").Error)
	require.Equal(t, "completed", checkRow.State)
	require.Equal(t, "outputs/tasks/completed-task/result.pdf", checkRow.ResultKey)

	// 2. Publishing task protection
	publishingRow := models.TaskCompletion{
		ID:             "publishing-task",
		OwnerIdentity:  "user:owner",
		DownloadToken:  "cap-pub",
		State:          "publishing",
		ResultKey:      "outputs/tasks/publishing-task/result.pdf",
		ResultURL:      "r2://outputs/tasks/publishing-task/result.pdf",
		ArtifactSHA256: "pub123sha",
		ArtifactBytes:  2048,
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(&publishingRow).Error)

	result, status, err = reg.CancelTask("publishing-task", "user:owner", "cap-pub")
	require.NoError(t, err)
	require.Equal(t, "TERMINAL_ALREADY", result)

	checkRow = models.TaskCompletion{}
	require.NoError(t, db.First(&checkRow, "id = ?", "publishing-task").Error)
	require.Equal(t, "publishing", checkRow.State)
	require.Equal(t, "outputs/tasks/publishing-task/result.pdf", checkRow.ResultKey)
}

func TestDurableCancellation_ConcurrentCancellationAndPublication(t *testing.T) {
	db, reg, _ := setupTasksPostgresDB(t)

	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())

	const taskID = "concurrent-race-task"
	const capToken = "cap-race-token"
	const owner = "user:racing"
	const key = "outputs/tasks/" + taskID + "/result.pdf"

	row := models.TaskCompletion{
		ID:            taskID,
		OwnerIdentity: owner,
		DownloadToken: capToken,
		State:         "active",
		ExpiresAt:     time.Now().Add(time.Hour),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, db.Create(&row).Error)

	srcFile := filepath.Join(t.TempDir(), "source.pdf")
	require.NoError(t, os.WriteFile(srcFile, []byte("%PDF-1.4 synthetic content"), 0644))

	const concurrentWorkers = 8
	var wg sync.WaitGroup
	var cancelWins int32
	var publishWins int32

	startCh := make(chan struct{})

	for i := 0; i < concurrentWorkers; i++ {
		wg.Add(1)
		if i%2 == 0 {
			// Canceller
			go func() {
				defer wg.Done()
				<-startCh
				res, _, cancelErr := reg.CancelTask(taskID, owner, capToken)
				if cancelErr == nil && res == "CANCELLED_SUCCESS" {
					atomic.AddInt32(&cancelWins, 1)
				}
			}()
		} else {
			// Publisher
			go func() {
				defer wg.Done()
				<-startCh
				pubErr := reg.PublishArtifact(context.Background(), taskID, key, srcFile, "application/pdf")
				if pubErr == nil {
					atomic.AddInt32(&publishWins, 1)
				}
			}()
		}
	}

	close(startCh)
	wg.Wait()

	var finalRow models.TaskCompletion
	require.NoError(t, db.First(&finalRow, "id = ?", taskID).Error)

	// Exactly one outcome must prevail: either cancelled or completed.
	// A cancelled task must NEVER be completed, and a completed task must NEVER be cancelled.
	if finalRow.State == "cancelled" {
		require.Equal(t, int32(1), atomic.LoadInt32(&cancelWins))
		require.Equal(t, int32(0), atomic.LoadInt32(&publishWins))
		require.Equal(t, "Task processing cancelled by user.", finalRow.Error)
	} else if finalRow.State == "completed" {
		require.Equal(t, int32(0), atomic.LoadInt32(&cancelWins))
		require.GreaterOrEqual(t, atomic.LoadInt32(&publishWins), int32(1))
		require.Equal(t, key, finalRow.ResultKey)
	} else {
		t.Fatalf("Unexpected terminal state in database: %s", finalRow.State)
	}
}

func TestDurableCancellation_HTTPRoutes(t *testing.T) {
	db, reg, _ := setupTasksPostgresDB(t)
	oldReg := Registry
	Registry = reg
	t.Cleanup(func() { Registry = oldReg })

	const taskID = "route-test-task"
	const capToken = "cap-route-test-token"
	const reservationID = "res-route-123"

	row := models.TaskCompletion{
		ID:              taskID,
		OwnerIdentity:   "",
		DownloadToken:   capToken,
		ReservationID:   reservationID,
		ReservationKind: "guest",
		State:           "active",
		ExpiresAt:       time.Now().Add(time.Hour),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, db.Create(&row).Error)

	var billingCancelledKind string
	var billingCancelledRes string
	var billingMu sync.Mutex

	CancelTaskBillingHandlerWithKind = func(resID, kind string) {
		billingMu.Lock()
		defer billingMu.Unlock()
		billingCancelledRes = resID
		billingCancelledKind = kind
	}
	t.Cleanup(func() { CancelTaskBillingHandlerWithKind = nil })

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return c.Next()
	})
	RegisterRoutes(app)

	// 1. DELETE without capability token -> 403 Forbidden
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/tasks/%s", taskID), nil)
	req.Header.Set("X-Test-Identity", "random-guest")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	var errResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, "FORBIDDEN", errResp["code"])

	billingMu.Lock()
	require.Empty(t, billingCancelledRes, "billing must not be cancelled on 403")
	billingMu.Unlock()

	var checkRow models.TaskCompletion
	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "active", checkRow.State)

	// 2. DELETE with valid capability token -> 200 OK
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/tasks/%s", taskID), nil)
	req.Header.Set("X-Test-Identity", "random-guest")
	req.Header.Set("X-Download-Token", capToken)
	resp, err = app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var successResp map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&successResp))
	require.Equal(t, "CANCELLED", successResp["status"])

	billingMu.Lock()
	require.Equal(t, reservationID, billingCancelledRes)
	require.Equal(t, "guest", billingCancelledKind)
	billingMu.Unlock()

	require.NoError(t, db.First(&checkRow, "id = ?", taskID).Error)
	require.Equal(t, "cancelled", checkRow.State)
}
