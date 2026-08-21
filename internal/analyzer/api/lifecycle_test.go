package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/analyzer/worker"
)

func setupLifecycleTestEnvironment(t *testing.T) (*Service, *fiber.App, *redis.Client, *miniredis.Miniredis, *gorm.DB) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	rClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rClient.Close() })

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("PostgreSQL database unavailable, skipping lifecycle test")
	}
	require.NoError(t, db.AutoMigrate(&models.AnalyzerSession{}))

	svc := NewService(db, rClient, worker.DefaultQueueName)
	// Use tight timeouts for rapid test execution
	svc.SetTimeouts(200*time.Millisecond, 2*time.Second, 50*time.Millisecond)

	ctrl := NewController(svc)
	app := fiber.New()
	RegisterRoutes(app.Group("/api"), ctrl)

	return svc, app, rClient, mr, db
}

func TestSubsystemReadiness_Endpoint(t *testing.T) {
	_, app, rClient, _, _ := setupLifecycleTestEnvironment(t)

	// 1. Initial State: No Workers -> 503 Service Unavailable
	req := httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/readiness", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var unavailResp SubsystemReadiness
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&unavailResp))
	assert.True(t, unavailResp.RedisReady)
	assert.False(t, unavailResp.WorkerReady)
	assert.Equal(t, 0, unavailResp.ActiveWorkers)

	// 2. Register Active Worker via Heartbeat
	workerCfg := worker.DefaultWorkerConfig()
	workerCfg.WorkerID = "worker-lifecycle-1"
	q, err := worker.NewRedisJobQueueWithClient(rClient, workerCfg)
	require.NoError(t, err)
	defer q.Close()

	err = q.SendHeartbeat(context.Background(), worker.WorkerInfo{
		WorkerID:    "worker-lifecycle-1",
		Hostname:    "test-host",
		PID:         5555,
		Concurrency: 4,
		StartedAt:   time.Now().UTC(),
		HeartbeatAt: time.Now().UTC(),
	}, 5*time.Second)
	require.NoError(t, err)

	// 3. Re-check Readiness -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/readiness", nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var readyResp SubsystemReadiness
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readyResp))
	assert.True(t, readyResp.IsReady)
	assert.True(t, readyResp.WorkerReady)
	assert.Equal(t, 1, readyResp.ActiveWorkers)
	assert.Equal(t, "worker-lifecycle-1", readyResp.Workers[0].WorkerID)
}

func TestWatchdog_StuckQueuedReconciliation(t *testing.T) {
	svc, app, rClient, mr, db := setupLifecycleTestEnvironment(t)

	// Start background watchdog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.StartWatchdog(ctx)

	// Create a session in QUEUED state with NO active workers
	sessionID := uuid.NewString()
	taskID := uuid.NewString()
	queuedTime := time.Now().UTC().Add(-500 * time.Millisecond) // Queued 500ms ago (> 200ms timeout)

	session := models.AnalyzerSession{
		ID:            sessionID,
		OwnerIdentity: "guest:tester",
		SourceType:    "git",
		CurrentTaskID: &taskID,
		Status:        "QUEUED",
		CreatedAt:     queuedTime,
		UpdatedAt:     queuedTime,
	}
	require.NoError(t, db.Create(&session).Error)

	initialProg := worker.TaskProgress{
		TaskID:          taskID,
		SessionID:       sessionID,
		Status:          worker.StatusQueued,
		ProgressPercent: 0,
		StageMessage:    "Analysis task queued",
		QueuedAt:        &queuedTime,
		UpdatedAt:       queuedTime,
	}
	data, _ := json.Marshal(initialProg)
	require.NoError(t, rClient.Set(context.Background(), fmt.Sprintf("pdfnest:task:%s", taskID), data, time.Hour).Err())

	// Wait for Watchdog to reconcile
	require.Eventually(t, func() bool {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/analyzer/tasks/%s", taskID), nil)
		req.Header.Set("X-Platen-Fingerprint", "tester") // maps to guest:tester
		resp, err := app.Test(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		var status TaskStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err == nil {
			return status.Status == worker.StatusFailed && status.ErrorMessage != ""
		}
		return false
	}, 2*time.Second, 50*time.Millisecond, "Watchdog must transition orphaned queued task to FAILED")

	// Verify DB state
	var updatedSession models.AnalyzerSession
	require.NoError(t, db.First(&updatedSession, "id = ?", sessionID).Error)
	assert.Equal(t, "FAILED", updatedSession.Status)
	_ = mr
}

func TestWatchdog_WorkerSaturationPreservedWithoutFalseFailure(t *testing.T) {
	svc, app, rClient, _, db := setupLifecycleTestEnvironment(t)

	// Register an active, healthy worker
	workerCfg := worker.DefaultWorkerConfig()
	workerCfg.WorkerID = "worker-busy-1"
	q, err := worker.NewRedisJobQueueWithClient(rClient, workerCfg)
	require.NoError(t, err)
	defer q.Close()

	err = q.SendHeartbeat(context.Background(), worker.WorkerInfo{
		WorkerID:    "worker-busy-1",
		Hostname:    "worker-node",
		PID:         1234,
		Concurrency: 4,
		ActiveJobs:  4, // All slots saturated
		StartedAt:   time.Now().UTC(),
		HeartbeatAt: time.Now().UTC(),
	}, 5*time.Second)
	require.NoError(t, err)

	// Create a legitimately waiting queued job
	sessionID := uuid.NewString()
	taskID := uuid.NewString()
	queuedTime := time.Now().UTC().Add(-300 * time.Millisecond) // Queued > 200ms, but worker is alive!

	session := models.AnalyzerSession{
		ID:            sessionID,
		OwnerIdentity: "guest:tester",
		SourceType:    "git",
		CurrentTaskID: &taskID,
		Status:        "QUEUED",
		CreatedAt:     queuedTime,
		UpdatedAt:     queuedTime,
	}
	require.NoError(t, db.Create(&session).Error)

	initialProg := worker.TaskProgress{
		TaskID:          taskID,
		SessionID:       sessionID,
		Status:          worker.StatusQueued,
		ProgressPercent: 0,
		StageMessage:    "Analysis task queued",
		QueuedAt:        &queuedTime,
		UpdatedAt:       queuedTime,
	}
	data, _ := json.Marshal(initialProg)
	require.NoError(t, rClient.Set(context.Background(), fmt.Sprintf("pdfnest:task:%s", taskID), data, time.Hour).Err())

	// Run reconciliation pass
	svc.ReconcileStaleTasks(context.Background())

	// Task MUST remain QUEUED (not falsely failed) because a healthy worker is active
	var check models.AnalyzerSession
	require.NoError(t, db.First(&check, "id = ?", sessionID).Error)
	assert.Equal(t, "QUEUED", check.Status, "Legitimately queued job under load must not be falsely failed")
	_ = app
}

func TestWatchdog_WorkerCrashedReconciliation(t *testing.T) {
	svc, _, rClient, _, db := setupLifecycleTestEnvironment(t)

	// Create a session in PROCESSING state assigned to a crashed worker
	sessionID := uuid.NewString()
	taskID := uuid.NewString()
	staleTime := time.Now().UTC().Add(-45 * time.Second) // Stale processing > 30s

	session := models.AnalyzerSession{
		ID:            sessionID,
		OwnerIdentity: "guest:tester",
		SourceType:    "git",
		CurrentTaskID: &taskID,
		Status:        "PROCESSING",
		CreatedAt:     staleTime,
		UpdatedAt:     staleTime,
	}
	require.NoError(t, db.Create(&session).Error)

	initialProg := worker.TaskProgress{
		TaskID:          taskID,
		SessionID:       sessionID,
		Status:          worker.StatusAnalyzing,
		ProgressPercent: 50,
		StageMessage:    "Analyzing AST facts",
		WorkerID:        "worker-crashed-vanished",
		UpdatedAt:       staleTime,
	}
	data, _ := json.Marshal(initialProg)
	require.NoError(t, rClient.Set(context.Background(), fmt.Sprintf("pdfnest:task:%s", taskID), data, time.Hour).Err())

	// Run reconciliation pass
	svc.ReconcileStaleTasks(context.Background())

	// Task must be marked FAILED due to vanished worker
	var updatedSession models.AnalyzerSession
	require.NoError(t, db.First(&updatedSession, "id = ?", sessionID).Error)
	assert.Equal(t, "FAILED", updatedSession.Status, "Orphaned job from crashed worker must transition to FAILED")

	var prog worker.TaskProgress
	rawProg, err := rClient.Get(context.Background(), fmt.Sprintf("pdfnest:task:%s", taskID)).Result()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(rawProg), &prog))
	assert.Equal(t, worker.StatusFailed, prog.Status)
	assert.Contains(t, prog.ErrorMessage, "terminated unexpectedly")
}
