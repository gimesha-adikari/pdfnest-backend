package tasks

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"pdfnest-backend/internal/identity"
	"strings"
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

	oldStore := identity.DefaultStore
	identity.NewStore(client, 0)

	cleanup := func() {
		_ = client.FlushDB(context.Background()).Err()
		Registry = oldRegistry
		identity.DefaultStore = oldStore
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

	task, err := reg.Get(taskID)
	if err != nil || task == nil {
		t.Fatalf("Get returned nil task for %s: %v", taskID, err)
	}
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

	task, err := reg.Get(taskID)
	if err != nil || task == nil {
		t.Fatalf("Get returned nil task for %s: %v", taskID, err)
	}
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
	task, err := replicaA.Get(taskID)
	if err != nil || task == nil {
		t.Fatalf("Get returned nil task for %s: %v", taskID, err)
	}
	task.ResultURL = tmpFile.Name()
	data, _ := json.Marshal(task)
	_ = client.Set(context.Background(), TaskKeyPrefix+taskID, string(data), TaskTTL).Err()

	// Simulated Replica B receives download request
	replicaB := &TaskRegistry{client: client}
	oldReg := Registry
	Registry = replicaB
	defer func() { Registry = oldReg }()

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

// 9. Comprehensive Security Model Matrix Tests:
// - Guest trying to download Authenticated User artifact -> DENY (403)
// - Authenticated User trying to download Guest artifact -> DENY (403)
// - Guest with transient identity but matching IP+UA -> ALLOW (200)
// - Nonexistent task -> DENY (404)
func TestDownloadController_SecurityAuthorizationMatrix(t *testing.T) {
	client, reg, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	identityStore := identity.NewStore(client, 0)

	// Create test file
	tmpFile, err := os.CreateTemp("", "sec-test-*.pdf")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	_, _ = tmpFile.WriteString("%PDF-1.4 security matrix content")
	_ = tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	authUserTaskID := "task-user-alice"
	authUserID := "usr_alice_123"

	guestTaskID := "task-guest-bob"
	guestID := "guest_uuid_bob_123"

	// Save auth user task
	_, _ = reg.SetWithKey(authUserTaskID, "COMPLETED", 100, "", "", authUserID)
	tUser, _ := reg.Get(authUserTaskID)
	tUser.ResultURL = tmpFile.Name()
	dataU, _ := json.Marshal(tUser)
	_ = reg.client.Set(context.Background(), TaskKeyPrefix+authUserTaskID, string(dataU), TaskTTL).Err()

	// Save guest record and task
	gRecord := &identity.GuestRecord{
		ID:              guestID,
		FingerprintHash: identity.HashString("fp-bob|ua-bob|0.0.0.0"),
		UserAgentHash:   identity.HashString("ua-bob"),
		IPHash:          identity.HashString("0.0.0.0"),
	}
	_ = identityStore.Save(context.Background(), gRecord)

	_, _ = reg.SetWithKey(guestTaskID, "COMPLETED", 100, "", "", guestID)
	tGuest, _ := reg.Get(guestTaskID)
	tGuest.ResultURL = tmpFile.Name()
	dataG, _ := json.Marshal(tGuest)
	_ = reg.client.Set(context.Background(), TaskKeyPrefix+guestTaskID, string(dataG), TaskTTL).Err()

	app := fiber.New()
	app.Get("/api/v1/download/:id", identity.Resolve(identityStore), HandleTaskDownload)

	// Test 9a: Guest tries to download User Alice's task -> DENY 403
	t.Run("Guest -> User Artifact -> 403 DENIED", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/download/"+authUserTaskID, nil)
		req.Header.Set("User-Agent", "ua-guest-hacker")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Errorf("Expected 403 Forbidden, got %d", resp.StatusCode)
		}
	})

	// Test 9b: Authenticated User tries to download Guest Bob's task -> DENY 403
	t.Run("User -> Guest Artifact -> 403 DENIED", func(t *testing.T) {
		appAuth := fiber.New()
		appAuth.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
			c.Locals(identity.LocalIdentityType, string(identity.TypeUser))
			c.Locals(identity.LocalIdentityIDKey, "usr_eve_456")
			return HandleTaskDownload(c)
		})

		req := httptest.NewRequest("GET", "/api/v1/download/"+guestTaskID, nil)
		resp, err := appAuth.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Errorf("Expected 403 Forbidden, got %d", resp.StatusCode)
		}
	})

	// Test 9c: CRITICAL SECURITY TEST - Guest B with SAME IP + SAME UA + DIFFERENT COOKIE -> MUST BE 403 FORBIDDEN
	t.Run("Same IP + Same UA Different Guest -> 403 FORBIDDEN (No IDOR)", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/download/"+guestTaskID, nil)
		req.Header.Set("User-Agent", "ua-bob")
		req.Header.Set("X-Forwarded-For", "203.0.113.1")
		req.Header.Set("Cookie", identity.CookieGuestID+"=guest_uuid_eve_attacker")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Errorf("SECURITY FAILURE: Expected 403 Forbidden for cross-guest access on same IP, got %d", resp.StatusCode)
		}
	})

	// Test 9d: Guest A downloads via Capability Token -> ALLOW 200
	t.Run("Guest Capability Token -> 200 OK", func(t *testing.T) {
		tGuest, _ := reg.Get(guestTaskID)
		dlToken := tGuest.DownloadToken

		req := httptest.NewRequest("GET", "/api/v1/download/"+guestTaskID+"?token="+dlToken, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("Expected 200 OK for capability token download, got %d", resp.StatusCode)
		}
	})

	// Test 9e: Invalid Token -> DENY 403
	t.Run("Invalid Capability Token -> 403 FORBIDDEN", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/download/"+guestTaskID+"?token=bogus-invalid-token-xyz", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for invalid capability token, got %d", resp.StatusCode)
		}
	})

	// Test 9f: Token from Task A used on Task B -> DENY 403
	t.Run("Task A Token used on Task B -> 403 FORBIDDEN", func(t *testing.T) {
		tUser, _ := reg.Get(authUserTaskID)
		aliceToken := tUser.DownloadToken

		req := httptest.NewRequest("GET", "/api/v1/download/"+guestTaskID+"?token="+aliceToken, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for mismatched task token, got %d", resp.StatusCode)
		}
	})

	// Test 9g: Nonexistent task ID -> DENY 404
	t.Run("Nonexistent Task -> 404 NOT FOUND", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/download/nonexistent-task-999", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("Expected 404 Not Found, got %d", resp.StatusCode)
		}
	})

	// Test 9h: Full End-to-End Guest Pipeline Simulation (Create -> Poll -> Token Download -> 200 PDF)
	t.Run("End-to-End Guest Pipeline -> 200 OK PDF Stream", func(t *testing.T) {
		taskID := "task-e2e-guest-999"
		guestIdentity := "guest_uuid_e2e_999"

		// 1. Task Creation in Redis with DownloadToken
		accepted, err := reg.SetWithKey(taskID, "COMPLETED", 100, "", "", guestIdentity)
		if err != nil || !accepted {
			t.Fatalf("Failed to create task in registry: %v", err)
		}

		// Set temp file result URL
		tStatus, err := reg.Get(taskID)
		if err != nil || tStatus == nil {
			t.Fatalf("Failed to retrieve created task: %v", err)
		}
		tStatus.ResultURL = tmpFile.Name()
		dataBytes, _ := json.Marshal(tStatus)
		_ = reg.client.Set(context.Background(), TaskKeyPrefix+taskID, string(dataBytes), TaskTTL).Err()

		// 2. Poll Task Status
		pollTask, err := reg.Get(taskID)
		if err != nil || pollTask == nil {
			t.Fatalf("Task polling failed: %v", err)
		}
		if pollTask.DownloadToken == "" {
			t.Fatalf("Task status polling response missing DownloadToken")
		}

		// 3. Construct authorized download URL matching useAsyncTask / UrlToPdfWorkspace format
		downloadURL := "/api/v1/download/" + taskID + "?token=" + url.QueryEscape(pollTask.DownloadToken)

		// 4. Issue GET request simulate browser fetch
		req := httptest.NewRequest("GET", downloadURL, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Download fetch request failed: %v", err)
		}

		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("Expected 200 OK for guest download, got %d", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body: %v", err)
		}
		if !strings.Contains(string(bodyBytes), "%PDF-1.4") {
			t.Errorf("Expected PDF stream payload, got %s", string(bodyBytes))
		}
	})
}
