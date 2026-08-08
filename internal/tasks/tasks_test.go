package tasks

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

func TestTaskRegistry_RedisOperations(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping integration test")
	}
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
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running, skipping test")
	}
	defer client.FlushDB(context.Background())

	regA := &TaskRegistry{client: client}
	regB := &TaskRegistry{client: client}

	taskID := "replica-task-999"

	// Replica A creates task
	if err := regA.Set(taskID, "PROCESSING", 40, "Working on replica A", ""); err != nil {
		t.Fatalf("Replica A set failed: %v", err)
	}

	// Replica B reads task created by Replica A
	tB, err := regB.Get(taskID)
	if err != nil || tB == nil {
		t.Fatalf("Replica B failed to read task: %v", err)
	}
	if tB.Status != "PROCESSING" || tB.Progress != 40 {
		t.Errorf("Replica B read incorrect state: %+v", tB)
	}

	// Replica B updates task
	if err := regB.Set(taskID, "COMPLETED", 100, "/tmp/replicaB.pdf", ""); err != nil {
		t.Fatalf("Replica B update failed: %v", err)
	}

	// Replica A reads updated state
	tA, err := regA.Get(taskID)
	if err != nil || tA == nil {
		t.Fatalf("Replica A failed to read updated task: %v", err)
	}
	if tA.Status != "COMPLETED" {
		t.Errorf("Replica A read stale state: %+v", tA)
	}
}

func TestTaskRegistry_RedisOutageNoFallback(t *testing.T) {
	// Point client to closed/invalid port
	badClient := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:59999", // Unused port
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

	// Verify HTTP 503 response on handler when Redis unavailable
	app := fiber.New()
	app.Get("/api/v1/tasks/:id", handleGetTaskStatus)

	// Inject broken registry for test
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
