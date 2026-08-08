package tasks

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping test")
	}
	_ = client.FlushDB(context.Background()).Err()
	return client
}

func TestTaskRegistry_RedisOperations(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "test-task-123"

	// 1. Get non-existent task
	t1, err := reg.Get(taskID)
	if err != nil {
		t.Fatalf("Expected nil error for missing task, got: %v", err)
	}
	if t1 != nil {
		t.Fatalf("Expected nil for missing task, got: %+v", t1)
	}

	// 2. Set task state
	if err := reg.Set(taskID, "PENDING", 0, "Ingesting document...", ""); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// 3. Get created task
	t2, err := reg.Get(taskID)
	if err != nil || t2 == nil {
		t.Fatalf("Failed to retrieve created task: %v", err)
	}
	if t2.Status != "PENDING" || t2.Progress != 0 {
		t.Errorf("Unexpected status/progress: %+v", t2)
	}

	// 4. Update task state
	if err := reg.Set(taskID, "COMPLETED", 100, "/tmp/output.pdf", ""); err != nil {
		t.Fatalf("Set completion failed: %v", err)
	}

	t3, err := reg.Get(taskID)
	if err != nil || t3 == nil {
		t.Fatalf("Failed to retrieve updated task: %v", err)
	}
	if t3.Status != "COMPLETED" || t3.ResultURL != "/tmp/output.pdf" {
		t.Errorf("Unexpected updated status: %+v", t3)
	}

	// 5. Verify TTL set
	ttl := client.TTL(context.Background(), TaskKeyPrefix+taskID).Val()
	if ttl <= 0 || ttl > TaskTTL {
		t.Errorf("Expected TTL close to 1 hour, got: %v", ttl)
	}
}

func TestTaskRegistry_MultiInstance(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	regA := &TaskRegistry{client: client}
	regB := &TaskRegistry{client: client}

	taskID := "replica-task-999"

	if err := regA.Set(taskID, "PROCESSING", 40, "Working on replica A", ""); err != nil {
		t.Fatalf("Replica A set failed: %v", err)
	}

	tB, err := regB.Get(taskID)
	if err != nil || tB == nil {
		t.Fatalf("Replica B failed to read task: %v", err)
	}
	if tB.Status != "PROCESSING" || tB.Progress != 40 {
		t.Errorf("Replica B read incorrect state: %+v", tB)
	}

	if err := regB.Set(taskID, "COMPLETED", 100, "/tmp/replicaB.pdf", ""); err != nil {
		t.Fatalf("Replica B update failed: %v", err)
	}

	tA, err := regA.Get(taskID)
	if err != nil || tA == nil {
		t.Fatalf("Replica A failed to read updated task: %v", err)
	}
	if tA.Status != "COMPLETED" {
		t.Errorf("Replica A read stale state: %+v", tA)
	}
}

func TestTaskRegistry_StaleTaskTransitionsToFailed(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "stale-task-001"

	// Create task manually in Redis with UpdatedAt = now - 1000s (> 900s threshold)
	staleTime := time.Now().Unix() - 1000
	staleTask := &TaskStatus{
		ID:        taskID,
		Status:    "PROCESSING",
		Progress:  30,
		UpdatedAt: staleTime,
	}
	data, _ := json.Marshal(staleTask)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	// Get() should detect stale status and transition to FAILED
	tRead, err := reg.Get(taskID)
	if err != nil || tRead == nil {
		t.Fatalf("Get() failed for stale task: %v", err)
	}

	if tRead.Status != "FAILED" {
		t.Errorf("Expected stale task status to be FAILED, got: %s", tRead.Status)
	}
	if tRead.Error == "" {
		t.Errorf("Expected timeout error message on stale task")
	}

	// Verify Redis TTL preserved
	ttl := client.TTL(context.Background(), TaskKeyPrefix+taskID).Val()
	if ttl <= 0 || ttl > TaskTTL {
		t.Errorf("Expected valid TTL preserved, got %v", ttl)
	}
}

func TestTaskRegistry_HealthyLongRunningTask(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "healthy-long-task"

	// Create task 10 minutes ago
	staleTime := time.Now().Unix() - 600
	task := &TaskStatus{
		ID:        taskID,
		Status:    "PROCESSING",
		Progress:  50,
		UpdatedAt: staleTime,
	}
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	// Worker updates progress 1 second ago
	if err := reg.Set(taskID, "PROCESSING", 70, "", ""); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get() should return PROCESSING (not marked stale!)
	tRead, err := reg.Get(taskID)
	if err != nil || tRead == nil {
		t.Fatalf("Get failed: %v", err)
	}
	if tRead.Status != "PROCESSING" || tRead.Progress != 70 {
		t.Errorf("Expected status PROCESSING with progress 70, got status=%s, prog=%d", tRead.Status, tRead.Progress)
	}
}

func TestTaskRegistry_TerminalStateImmutability(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "terminal-task-1"

	// 1. Complete task
	_ = reg.Set(taskID, "COMPLETED", 100, "/tmp/out.pdf", "")

	// 2. Late worker attempts to write FAILED -> Should be REJECTED
	_ = reg.Set(taskID, "FAILED", 0, "", "Late failure error")

	tRead, _ := reg.Get(taskID)
	if tRead.Status != "COMPLETED" {
		t.Errorf("Expected terminal status COMPLETED to remain immutable, got: %s", tRead.Status)
	}

	// 3. Mark task FAILED
	taskID2 := "terminal-task-2"
	_ = reg.Set(taskID2, "FAILED", 0, "", "Original error")

	// 4. Late worker attempts to write COMPLETED -> Should be REJECTED
	_ = reg.Set(taskID2, "COMPLETED", 100, "/tmp/out2.pdf", "")

	tRead2, _ := reg.Get(taskID2)
	if tRead2.Status != "FAILED" {
		t.Errorf("Expected terminal status FAILED to remain immutable, got: %s", tRead2.Status)
	}
}

func TestTaskRegistry_MonotonicProgress(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "monotonic-task"

	_ = reg.Set(taskID, "PROCESSING", 60, "", "")

	// Late worker attempts to regress progress to 30 -> Should keep 60
	_ = reg.Set(taskID, "PROCESSING", 30, "", "")

	tRead, _ := reg.Get(taskID)
	if tRead.Progress != 60 {
		t.Errorf("Expected progress to remain monotonic at 60, got: %d", tRead.Progress)
	}
}

func TestTaskRegistry_ConcurrentWorkerVsStaleDetectorRace(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "race-task"

	// Create stale task (> 900s)
	staleTime := time.Now().Unix() - 1000
	task := &TaskStatus{
		ID:        taskID,
		Status:    "PROCESSING",
		Progress:  40,
		UpdatedAt: staleTime,
	}
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: Stale Detector (Get)
	go func() {
		defer wg.Done()
		_, _ = reg.Get(taskID)
	}()

	// Goroutine 2: Worker Completion (Set COMPLETED)
	go func() {
		defer wg.Done()
		_ = reg.Set(taskID, "COMPLETED", 100, "/tmp/done.pdf", "")
	}()

	wg.Wait()

	tFinal, err := reg.Get(taskID)
	if err != nil || tFinal == nil {
		t.Fatalf("Failed to read final task state: %v", err)
	}

	// Final status MUST be either FAILED or COMPLETED (terminal!), never invalid or unreadable
	if tFinal.Status != "FAILED" && tFinal.Status != "COMPLETED" {
		t.Errorf("Expected valid terminal status, got: %s", tFinal.Status)
	}
}

func TestTaskRegistry_RedisOutageNoFallback(t *testing.T) {
	badClient := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:59999",
		DialTimeout: 50 * time.Millisecond,
		MaxRetries:  -1,
	})
	reg := &TaskRegistry{client: badClient}

	_, err := reg.Get("any-task")
	if err == nil {
		t.Fatalf("Expected error when Redis is unreachable, got nil")
	}

	err = reg.Set("any-task", "PENDING", 0, "", "")
	if err == nil {
		t.Fatalf("Expected error on Set when Redis is unreachable, got nil")
	}

	app := fiber.New()
	app.Get("/api/v1/tasks/:id", handleGetTaskStatus)

	oldRegistry := Registry
	Registry = reg
	defer func() { Registry = oldRegistry }()

	req := httptest.NewRequest("GET", "/api/v1/tasks/123", nil)
	resp, err := app.Test(req, 3000)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}

	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("Expected status 503 Service Unavailable, got %d", resp.StatusCode)
	}
}

func TestTaskRegistry_GetWithTransition_Stale(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "stale-task-res-1"
	resID := "res-uuid-999"

	staleTime := time.Now().Unix() - 1000
	task := &TaskStatus{
		ID:            taskID,
		Status:        "PROCESSING",
		Progress:      30,
		ReservationID: resID,
		UpdatedAt:     staleTime,
	}
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	taskRead, stalePerformed, reservationID, err := reg.GetWithTransition(taskID)
	if err != nil || taskRead == nil {
		t.Fatalf("GetWithTransition failed: %v", err)
	}

	if !stalePerformed {
		t.Errorf("Expected stalePerformed == true, got false")
	}
	if reservationID != resID {
		t.Errorf("Expected reservationID '%s', got '%s'", resID, reservationID)
	}
	if taskRead.Status != "FAILED" {
		t.Errorf("Expected status FAILED after stale transition, got: %s", taskRead.Status)
	}
}

func TestTaskRegistry_GetWithTransition_PendingStale(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "pending-stale-task-1"
	resID := "res-uuid-888"

	staleTime := time.Now().Unix() - 1000
	task := &TaskStatus{
		ID:            taskID,
		Status:        "PENDING",
		Progress:      0,
		ReservationID: resID,
		UpdatedAt:     staleTime,
	}
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	taskRead, stalePerformed, reservationID, err := reg.GetWithTransition(taskID)
	if err != nil || taskRead == nil {
		t.Fatalf("GetWithTransition failed: %v", err)
	}

	if !stalePerformed {
		t.Errorf("Expected PENDING stale task to trigger stalePerformed == true")
	}
	if reservationID != resID {
		t.Errorf("Expected reservationID '%s', got '%s'", resID, reservationID)
	}
	if taskRead.Status != "FAILED" {
		t.Errorf("Expected status FAILED for stale PENDING task, got: %s", taskRead.Status)
	}
}

func TestTaskRegistry_GetWithTransition_MultiReplica(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "multi-replica-stale-task"
	resID := "res-multi-123"

	staleTime := time.Now().Unix() - 1000
	task := &TaskStatus{
		ID:            taskID,
		Status:        "PROCESSING",
		Progress:      20,
		ReservationID: resID,
		UpdatedAt:     staleTime,
	}
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	var wg sync.WaitGroup
	var winnerCount int64

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, stalePerformed, returnedResID, err := reg.GetWithTransition(taskID)
			if err == nil && stalePerformed && returnedResID == resID {
				atomic.AddInt64(&winnerCount, 1)
			}
		}()
	}
	wg.Wait()

	if atomic.LoadInt64(&winnerCount) != 1 {
		t.Errorf("Expected EXACTLY ONE replica to receive staleTransitionPerformed == true, got %d", winnerCount)
	}
}

func TestTaskRegistry_CancelTask(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	taskID := "cancel-test-task"
	ownerID := "user:alice"

	// 1. Cancel non-existent task
	res, tStatus, err := reg.CancelTask(taskID, ownerID)
	if err != nil || res != "NOT_FOUND" || tStatus != nil {
		t.Fatalf("Expected NOT_FOUND for missing task, got res=%s, status=%v, err=%v", res, tStatus, err)
	}

	// 2. Set task to PROCESSING with ownerID
	okCreated, err := reg.SetWithKey(taskID, "PROCESSING", 30, "", "", ownerID, "res-123")
	if err != nil || !okCreated {
		t.Fatalf("SetWithKey failed: %v", err)
	}

	// 3. Unauthorized cancel attempt by Bob
	resAuth, _, errAuth := reg.CancelTask(taskID, "user:bob")
	if errAuth != nil || resAuth != "UNAUTHORIZED" {
		t.Fatalf("Expected UNAUTHORIZED for Bob's attempt, got res=%s, err=%v", resAuth, errAuth)
	}

	// 4. Authorized cancel attempt by Alice
	resCancel, statusCancel, errCancel := reg.CancelTask(taskID, ownerID)
	if errCancel != nil || resCancel != "CANCELLED_SUCCESS" || statusCancel == nil {
		t.Fatalf("Expected CANCELLED_SUCCESS for Alice, got res=%s, status=%v, err=%v", resCancel, statusCancel, errCancel)
	}
	if statusCancel.Status != "CANCELLED" {
		t.Errorf("Expected status CANCELLED, got %s", statusCancel.Status)
	}

	// 5. Subsequent SetWithKey COMPLETED rejected
	accepted, err := reg.SetWithKey(taskID, "COMPLETED", 100, "key.pdf", "", ownerID)
	if err != nil || accepted {
		t.Errorf("Expected SetWithKey COMPLETED to be rejected on CANCELLED task, got accepted=%v", accepted)
	}

	// 6. Repeated cancel is idempotent (TERMINAL_ALREADY)
	resRepeat, _, errRepeat := reg.CancelTask(taskID, ownerID)
	if errRepeat != nil || resRepeat != "TERMINAL_ALREADY" {
		t.Errorf("Expected TERMINAL_ALREADY on repeated cancel, got res=%s, err=%v", resRepeat, errRepeat)
	}
}
