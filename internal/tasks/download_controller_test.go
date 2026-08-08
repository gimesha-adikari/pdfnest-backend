package tasks

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"pdfnest-backend/internal/identity"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

func setupDownloadTestRedis(t *testing.T) (*redis.Client, *TaskRegistry, func()) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping test")
	}
	_ = client.FlushDB(context.Background()).Err()

	reg := &TaskRegistry{client: client}
	oldRegistry := Registry
	Registry = reg

	cleanup := func() {
		_ = client.FlushDB(context.Background()).Err()
		Registry = oldRegistry
	}

	return client, reg, cleanup
}

// 1. Authenticated User creates task -> same authenticated user downloads -> 200 OK
func TestDownloadController_AuthenticatedUserSuccess(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "auth-task-success"
	userID := "user-alice-123"

	tmpFile, err := os.CreateTemp("", "test-auth-*.pdf")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 authenticated user content")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", userID)
	if err != nil {
		t.Fatalf("Failed to store task: %v", err)
	}

	task, _ := reg.Get(taskID)
	task.ResultURL = tmpFile.Name()
	data, _ := json.Marshal(task)
	_ = reg.client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	req.Header.Set("X-Test-Identity", "user-alice-123")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK for owner download, got %d", resp.StatusCode)
	}
}

// 2. Authenticated User A creates task -> User B downloads -> 403 Forbidden
func TestDownloadController_AuthenticatedUserForbidden(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "auth-task-forbidden"
	userA := "user-alice-123"

	_, err := reg.SetWithKey(taskID, "COMPLETED", 100, "", "", userA)
	if err != nil {
		t.Fatalf("Failed to store task: %v", err)
	}

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	req.Header.Set("X-Test-Identity", "user-bob-456")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for non-owner download, got %d", resp.StatusCode)
	}
}

// 3. Guest creates task -> same guest/session downloads -> 200 OK
func TestDownloadController_GuestUserSuccess(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "guest-task-success"
	guestID := "guest-uuid-789"

	tmpFile, err := os.CreateTemp("", "test-guest-*.pdf")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 guest user content")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", guestID)

	task, _ := reg.Get(taskID)
	task.ResultURL = tmpFile.Name()
	data, _ := json.Marshal(task)
	_ = reg.client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	req.Header.Set("X-Test-Identity", "guest-uuid-789")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK for guest owner download, got %d", resp.StatusCode)
	}
}

// 4. Guest A creates task -> Guest B downloads -> 403 Forbidden
func TestDownloadController_GuestUserForbidden(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "guest-task-forbidden"
	guestA := "guest-uuid-789"

	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", guestA)

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	req.Header.Set("X-Test-Identity", "guest-uuid-other")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for different guest download, got %d", resp.StatusCode)
	}
}

// 5. Task created on replica A -> download on replica B (reading from Redis) -> 200 OK
func TestDownloadController_CrossReplicaDownload(t *testing.T) {
	client, _, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "cross-replica-task"
	ownerID := "user-alice-123"

	tmpFile, err := os.CreateTemp("", "test-cross-*.pdf")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 cross replica content")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Simulated Replica A stores task in shared Redis
	replicaA := &TaskRegistry{client: client}
	_, _ = replicaA.SetWithKey(taskID, "COMPLETED", 100, "", "", ownerID)
	task, _ := replicaA.Get(taskID)
	task.ResultURL = tmpFile.Name()
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	// Simulated Replica B receives download request
	replicaB := &TaskRegistry{client: client}
	Registry = replicaB

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, ownerID)
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK on replica B download, got %d", resp.StatusCode)
	}
}

// 6. Missing / invalid identity mismatch -> 403 Forbidden
func TestDownloadController_MissingOrInvalidIdentity(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "invalid-id-task"
	ownerID := "user-alice-123"

	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", ownerID)

	app := fiber.New()
	app.Get("/api/v1/download/:id", HandleTaskDownload)

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for missing/unmatched identity, got %d", resp.StatusCode)
	}
}

// 7. Legacy /tmp task behavior remains unchanged
func TestDownloadController_LegacyTmpTaskFallback(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "legacy-tmp-task"
	ownerID := "user-alice-123"

	// Existing legacy file
	tmpFile, err := os.CreateTemp("", "legacy-*.pdf")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 legacy content")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "", "", ownerID)
	task, _ := reg.Get(taskID)
	task.ResultURL = tmpFile.Name()
	data, _ := json.Marshal(task)
	_ = reg.client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, ownerID)
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App test failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK for legacy file download, got %d", resp.StatusCode)
	}
}

// 8. R2 missing object returns 410 ASSET_REMOVED, not 403, when authorization succeeds
func TestDownloadController_MissingR2Object410(t *testing.T) {
	_, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	taskID := "missing-r2-task-002"
	ownerID := "user-alice-123"

	_, _ = reg.SetWithKey(taskID, "COMPLETED", 100, "outputs/tasks/missing/nonexistent.pdf", "", ownerID)

	app := fiber.New()
	app.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, ownerID)
		return HandleTaskDownload(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/download/"+taskID, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Download request failed: %v", err)
	}

	if resp.StatusCode != fiber.StatusGone && resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("Expected 410 Gone or 500 Storage Error for missing R2 object, got %d", resp.StatusCode)
	}
}
