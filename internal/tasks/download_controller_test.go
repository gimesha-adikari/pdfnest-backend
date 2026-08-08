package tasks

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

func TestDownloadController_IdentityAuthorizationIsolation(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping test")
	}
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	oldRegistry := Registry
	Registry = reg
	defer func() { Registry = oldRegistry }()

	taskID := "auth-task-001"
	ownerID := "user-alice-123"

	// Create local file for download test
	tmpFile, err := os.CreateTemp("", "test-download-*.pdf")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 test payload")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Store task owned by user-alice-123
	_, err = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", ownerID)
	if err != nil {
		t.Fatalf("Failed to set task: %v", err)
	}
	// Manually set ResultURL to local file path for test
	task, _ := reg.Get(taskID)
	task.ResultURL = tmpFile.Name()
	data, _ := jsonMarshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		// Mock identity middleware setting requester identity
		reqIdentity := c.Get("Test-Identity-ID")
		if reqIdentity != "" {
			c.Locals("identity_id", reqIdentity)
		}
		return HandleTaskDownload(c)
	})

	// 1. Owner (user-alice-123) requests download -> Should return HTTP 200 OK
	reqOwner := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	reqOwner.Header.Set("Test-Identity-ID", "user-alice-123")

	respOwner, err := app.Test(reqOwner)
	if err != nil {
		t.Fatalf("Owner request failed: %v", err)
	}
	if respOwner.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK for owner download, got %d", respOwner.StatusCode)
	}

	// 2. Unrelated user (user-bob-456) requests download -> Should return HTTP 403 Forbidden
	reqBob := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	reqBob.Header.Set("Test-Identity-ID", "user-bob-456")

	respBob, err := app.Test(reqBob)
	if err != nil {
		t.Fatalf("Bob request failed: %v", err)
	}
	if respBob.StatusCode != fiber.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for non-owner download, got %d", respBob.StatusCode)
	}
}

func TestDownloadController_MissingR2Object410(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running, skipping test")
	}
	defer client.FlushDB(context.Background())

	reg := &TaskRegistry{client: client}
	oldRegistry := Registry
	Registry = reg
	defer func() { Registry = oldRegistry }()

	taskID := "missing-r2-task-002"
	ownerID := "user-alice-123"

	// Task COMPLETED with ResultKey pointing to non-existent R2 object
	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "outputs/tasks/missing/nonexistent.pdf", "", ownerID)

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals("identity_id", ownerID)
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Download request failed: %v", err)
	}

	// Should return 410 Gone / 500 Storage Error when R2 object is missing / R2 unconfigured
	if resp.StatusCode != fiber.StatusGone && resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("Expected 410 Gone or 500 Storage Error for missing R2 object, got %d", resp.StatusCode)
	}
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
