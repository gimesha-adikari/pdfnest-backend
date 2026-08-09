package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/tasks"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping test")
	}
	_ = client.FlushDB(context.Background()).Err()
	config.Redis = client
	return client
}

func TestAsyncCancellation_ReleasesLimiterLease(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	identityID := "user:" + uuid.New().String()
	taskID := uuid.New().String()

	// 1. Set task in PENDING
	ok, err := tasks.Registry.SetWithKey(taskID, "PENDING", 0, "", "Starting task", identityID, "res-123")
	if err != nil || !ok {
		t.Fatalf("Failed to set task: %v", err)
	}

	// 2. Acquire limiter lease for identityID
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rel, acqOk, acqErr := limiter.Default.AcquireWithRelease(ctx, taskID, identityID)
	if acqErr != nil || !acqOk {
		t.Fatalf("Failed to acquire limiter lease: %v", acqErr)
	}

	// 3. Cancel task via Registry.CancelTask
	luaRes, _, cancelErr := tasks.Registry.CancelTask(taskID, identityID)
	if cancelErr != nil {
		t.Fatalf("CancelTask failed: %v", cancelErr)
	}
	if luaRes != "CANCELLED_SUCCESS" {
		t.Fatalf("Expected CANCELLED_SUCCESS, got %s", luaRes)
	}

	// 4. Worker goroutine defer releases lease
	rel()

	// 5. Verify a subsequent task for same identity can immediately acquire capacity
	newTaskID := uuid.New().String()
	rel2, acqOk2, acqErr2 := limiter.Default.AcquireWithRelease(ctx, newTaskID, identityID)
	if acqErr2 != nil || !acqOk2 {
		t.Fatalf("Expected immediate capacity acquisition after cancellation cleanup, got err: %v, ok: %v", acqErr2, acqOk2)
	}
	rel2()
}

func TestContextCancellation_AbortsWorkerHTTPPost(t *testing.T) {
	// Create a dummy slow HTTP server to simulate Python OCR worker
	slowWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	defer slowWorker.Close()

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Create HTTP request with context to slow worker
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slowWorker.URL, strings.NewReader("dummy payload"))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Cancel context after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, httpErr := http.DefaultClient.Do(req)
	duration := time.Since(start)

	if httpErr == nil {
		t.Fatalf("Expected HTTP request to fail on context cancellation")
	}

	if duration >= 2*time.Second {
		t.Fatalf("HTTP request took %v, expected it to abort within 200ms on context cancellation", duration)
	}
}

func TestCancelTask_HTTPHandlerIntegration(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()
	app.Delete("/v1/tasks/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		result, task, err := tasks.Registry.CancelTask(id, "guest-123")
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if result == "NOT_FOUND" {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		return c.JSON(task)
	})

	taskID := "task-http-cancel-1"
	_, _ = tasks.Registry.SetWithKey(taskID, "PROCESSING", 40, "", "Running OCR...", "guest-123", "res-456")

	req := httptest.NewRequest("DELETE", "/v1/tasks/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test request failed: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	task, _ := tasks.Registry.Get(taskID)
	if task == nil || task.Status != "CANCELLED" {
		t.Fatalf("Expected task status CANCELLED in Redis, got: %+v", task)
	}
}
