package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/uploads"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 14})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Local Redis server not running on 127.0.0.1:6379, skipping test")
	}
	_ = client.FlushDB(context.Background()).Err()
	return client
}

func TestIdempotency_FirstRequestAndDuplicate(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()

	var handlerExecutions int64

	app.Post("/test-async", Use(client), func(c *fiber.Ctx) error {
		atomic.AddInt64(&handlerExecutions, 1)
		taskID := "task-uuid-12345"
		_ = SetTaskID(c, taskID, client)
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": taskID})
	})

	// 1. First Request with Idempotency-Key
	req1 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("sample body payload"))
	req1.Header.Set("Idempotency-Key", "idemp-key-1")
	req1.Header.Set("Content-Type", "text/plain")

	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}
	if resp1.StatusCode != fiber.StatusAccepted {
		t.Errorf("Expected 202 Accepted, got %d", resp1.StatusCode)
	}
	if atomic.LoadInt64(&handlerExecutions) != 1 {
		t.Errorf("Expected 1 handler execution, got %d", handlerExecutions)
	}

	// 2. Duplicate Request with SAME Idempotency-Key & SAME Payload
	req2 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("sample body payload"))
	req2.Header.Set("Idempotency-Key", "idemp-key-1")
	req2.Header.Set("Content-Type", "text/plain")

	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("Duplicate request failed: %v", err)
	}
	if resp2.StatusCode != fiber.StatusAccepted {
		t.Errorf("Expected 202 Accepted on duplicate, got %d", resp2.StatusCode)
	}
	// Handler execution count MUST still be 1 (bypassed execution!)
	if atomic.LoadInt64(&handlerExecutions) != 1 {
		t.Errorf("Expected handler execution count to remain 1, got %d", handlerExecutions)
	}
}

func TestIdempotency_ProcessingConflict(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()

	app.Post("/test-async", Use(client), func(c *fiber.Ctx) error {
		// Intentionally do NOT call SetTaskID immediately to leave state in PROCESSING
		return c.Status(fiber.StatusOK).SendString("processing held")
	})

	// First Request -> Sets PROCESSING lock
	req1 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("payload"))
	req1.Header.Set("Idempotency-Key", "lock-key-123")
	req1.Header.Set("Content-Type", "text/plain")
	_, _ = app.Test(req1)

	// Second Request with SAME key while still PROCESSING -> Should return 409 Conflict + Retry-After: 2
	req2 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("payload"))
	req2.Header.Set("Idempotency-Key", "lock-key-123")
	req2.Header.Set("Content-Type", "text/plain")

	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}
	if resp2.StatusCode != fiber.StatusConflict {
		t.Errorf("Expected 409 Conflict, got %d", resp2.StatusCode)
	}
	if resp2.Header.Get("Retry-After") != "2" {
		t.Errorf("Expected Retry-After: 2 header, got %s", resp2.Header.Get("Retry-After"))
	}
}

func TestIdempotency_PayloadMismatch422(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()

	app.Post("/test-async", Use(client), func(c *fiber.Ctx) error {
		_ = SetTaskID(c, "task-1", client)
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": "task-1"})
	})

	// Request 1 with payload A
	req1 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("Payload A"))
	req1.Header.Set("Idempotency-Key", "reuse-key")
	req1.Header.Set("Content-Type", "text/plain")
	_, _ = app.Test(req1)

	// Request 2 with SAME key but DIFFERENT payload B -> Should return HTTP 422
	req2 := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("Payload B - Different"))
	req2.Header.Set("Idempotency-Key", "reuse-key")
	req2.Header.Set("Content-Type", "text/plain")

	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("Request 2 failed: %v", err)
	}
	if resp2.StatusCode != fiber.StatusUnprocessableEntity {
		t.Errorf("Expected 422 Unprocessable Entity, got %d", resp2.StatusCode)
	}
}

func TestIdempotency_Concurrent10Race(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, "test-user-race")
		return c.Next()
	})

	var handlerExecutions int64

	app.Post("/test-async", Use(client), func(c *fiber.Ctx) error {
		atomic.AddInt64(&handlerExecutions, 1)
		_ = SetTaskID(c, "concurrent-task-id", client)
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"taskId": "concurrent-task-id"})
	})

	var wg sync.WaitGroup
	concurrentCount := 10
	wg.Add(concurrentCount)

	for i := 0; i < concurrentCount; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/test-async", bytes.NewBufferString("concurrent payload"))
			req.Header.Set("Idempotency-Key", "race-key-999")
			req.Header.Set("Content-Type", "text/plain")

			resp, err := app.Test(req, 3000)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	// Handler execution count MUST be exactly 1 despite 10 simultaneous requests!
	if atomic.LoadInt64(&handlerExecutions) != 1 {
		t.Errorf("Expected exactly 1 handler execution under race, got %d", handlerExecutions)
	}
}

func TestIdempotency_MultipartStreamSafety(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()

	app.Post("/test-multipart", Use(client), func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return c.Status(fiber.StatusBadRequest).SendString("Missing file")
		}
		if fileHeader.Size == 0 {
			return c.Status(fiber.StatusBadRequest).SendString("Empty file")
		}
		return c.Status(fiber.StatusOK).SendString("OK")
	})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test.pdf")
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}
	_, _ = part.Write([]byte("%PDF-1.4 sample pdf content header bytes"))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/test-multipart", body)
	req.Header.Set("Idempotency-Key", "multipart-key-1")
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Multipart request failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}
}

func TestCalculateFingerprint_MultipartIgnoresBoundary(t *testing.T) {
	app := fiber.New()
	fingerprints := make([]string, 0, 2)
	app.Post("/fingerprint", func(c *fiber.Ctx) error {
		fingerprint, err := CalculateFingerprint(c)
		if err != nil {
			return err
		}
		fingerprints = append(fingerprints, fingerprint)
		return c.SendStatus(fiber.StatusNoContent)
	})

	makeRequest := func(boundary string) *http.Request {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if err := writer.SetBoundary(boundary); err != nil {
			t.Fatalf("failed to set multipart boundary: %v", err)
		}
		part, err := writer.CreateFormFile("file", "same.pdf")
		if err != nil {
			t.Fatalf("failed to create file part: %v", err)
		}
		_, _ = part.Write([]byte("%PDF-1.4 same payload"))
		if err := writer.WriteField("language", "eng"); err != nil {
			t.Fatalf("failed to write language field: %v", err)
		}
		if err := writer.WriteField("routing_policy", "AUTO"); err != nil {
			t.Fatalf("failed to write routing field: %v", err)
		}
		_ = writer.Close()
		req := httptest.NewRequest("POST", "/fingerprint", body)
		req.Header.Set("Idempotency-Key", "multipart-boundary-key")
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	}

	for _, boundary := range []string{"----phase3e-boundary-a", "----phase3e-boundary-b"} {
		resp, err := app.Test(makeRequest(boundary))
		if err != nil {
			t.Fatalf("fingerprint request failed: %v", err)
		}
		if resp.StatusCode != fiber.StatusNoContent {
			t.Fatalf("expected 204 response, got %d", resp.StatusCode)
		}
	}
	if len(fingerprints) != 2 || fingerprints[0] != fingerprints[1] {
		t.Fatalf("same multipart payload must have stable fingerprint: %#v", fingerprints)
	}
}

func TestCalculateFingerprint_MultipartSemanticPayloadDifferencesConflict(t *testing.T) {
	app := fiber.New()
	fingerprints := make([]string, 0, 10)
	app.Post("/fingerprint-semantic", func(c *fiber.Ctx) error {
		fingerprint, err := CalculateFingerprint(c)
		if err != nil {
			return err
		}
		fingerprints = append(fingerprints, fingerprint)
		return c.SendStatus(fiber.StatusNoContent)
	})

	makeRequest := func(boundary, fileContent, language, routing string) *http.Request {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if err := writer.SetBoundary(boundary); err != nil {
			t.Fatalf("failed to set multipart boundary: %v", err)
		}
		part, err := writer.CreateFormFile("file", "same.pdf")
		if err != nil {
			t.Fatalf("failed to create file part: %v", err)
		}
		_, _ = part.Write([]byte(fileContent))
		if err := writer.WriteField("language", language); err != nil {
			t.Fatalf("failed to write language field: %v", err)
		}
		if err := writer.WriteField("routing_policy", routing); err != nil {
			t.Fatalf("failed to write routing field: %v", err)
		}
		_ = writer.Close()
		req := httptest.NewRequest("POST", "/fingerprint-semantic", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	}

	semanticBaseline := func(index int) string {
		return fingerprints[index]
	}
	send := func(req *http.Request) {
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("fingerprint request failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != fiber.StatusNoContent {
			t.Fatalf("expected 204 response, got %d", resp.StatusCode)
		}
	}

	send(makeRequest("----semantic-a", "%PDF-1.4 same payload", "eng", "AUTO"))
	send(makeRequest("----semantic-b", "%PDF-1.4 same payload", "eng", "AUTO"))
	if semanticBaseline(0) != semanticBaseline(1) {
		t.Fatal("same semantic multipart payload must have the same fingerprint")
	}

	send(makeRequest("----semantic-c", "%PDF-1.4 changed bytes", "eng", "AUTO"))
	if semanticBaseline(0) == semanticBaseline(2) {
		t.Fatal("different multipart file bytes must change the fingerprint")
	}

	send(makeRequest("----semantic-d", "%PDF-1.4 same payload", "sin", "AUTO"))
	if semanticBaseline(0) == semanticBaseline(3) {
		t.Fatal("different multipart language must change the fingerprint")
	}

	send(makeRequest("----semantic-e", "%PDF-1.4 same payload", "eng", "FAST"))
	if semanticBaseline(0) == semanticBaseline(4) {
		t.Fatal("different multipart routing policy must change the fingerprint")
	}
}

func TestCalculateFingerprint_OrderedMultipartFilesAffectFingerprint(t *testing.T) {
	app := fiber.New()
	fingerprints := make([]string, 0, 2)
	app.Post("/ordered", uploads.Prepare(), func(c *fiber.Ctx) error {
		fingerprint, err := CalculateFingerprint(c)
		if err != nil {
			return err
		}
		fingerprints = append(fingerprints, fingerprint)
		return c.SendStatus(fiber.StatusNoContent)
	})

	makeRequest := func(first, second string) *http.Request {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		for _, item := range []struct{ name, content string }{{"first.png", first}, {"second.png", second}} {
			part, err := writer.CreateFormFile("file", item.name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte(item.content))
		}
		_ = writer.WriteField("language", "eng")
		_ = writer.Close()
		req := httptest.NewRequest("POST", "/ordered", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	}

	for _, request := range []*http.Request{
		makeRequest("image-one", "image-two"),
		makeRequest("image-two", "image-one"),
	} {
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("ordered fingerprint request failed: %v", err)
		}
		if response == nil || response.StatusCode != fiber.StatusNoContent {
			t.Fatalf("ordered fingerprint request failed: response=%v", response)
		}
	}
	if len(fingerprints) != 2 || fingerprints[0] == fingerprints[1] {
		t.Fatalf("reordering image inputs must change fingerprint: %#v", fingerprints)
	}
}

func TestIdempotency_AtomicRelease_Processing(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, "test-user-release")
		return c.Next()
	})

	app.Post("/test-release", Use(client), func(c *fiber.Ctx) error {
		// Simulate failure before admission: call Release
		Release(c, client)
		return c.Status(fiber.StatusBadRequest).SendString("releasing")
	})

	req := httptest.NewRequest("POST", "/test-release", bytes.NewBufferString("test body"))
	req.Header.Set("Idempotency-Key", "release-test-key")
	req.Header.Set("Content-Type", "text/plain")

	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("Request failed: %v, status: %d", err, resp.StatusCode)
	}

	// Verify key was DELETED from Redis
	val, err := client.Get(context.Background(), "pdfnest:idempotency:test-user-release:release-test-key").Result()
	if err == nil && val != "" {
		t.Errorf("Expected key to be deleted by Release, but found: %s", val)
	}
}

func TestIdempotency_AtomicRelease_CreatedNoOp(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, "test-user-created")
		return c.Next()
	})

	app.Post("/test-created-release", Use(client), func(c *fiber.Ctx) error {
		_ = SetTaskID(c, "task-999", client)
		// Call Release after CREATED set -> must be NO-OP!
		Release(c, client)
		return c.Status(fiber.StatusAccepted).SendString("created")
	})

	req := httptest.NewRequest("POST", "/test-created-release", bytes.NewBufferString("test body"))
	req.Header.Set("Idempotency-Key", "created-key-999")
	req.Header.Set("Content-Type", "text/plain")

	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != fiber.StatusAccepted {
		t.Fatalf("Request failed: %v, status: %d", err, resp.StatusCode)
	}

	// Verify key still EXISTS in Redis with state CREATED!
	val, err := client.Get(context.Background(), "pdfnest:idempotency:test-user-created:created-key-999").Result()
	if err != nil || val == "" {
		t.Fatalf("Expected key to remain intact after SetTaskID, got err: %v", err)
	}

	if !bytes.Contains([]byte(val), []byte("CREATED")) {
		t.Errorf("Expected state CREATED in stored record, got: %s", val)
	}
}

func TestIdempotency_ConcurrentSetTaskIDVsRelease(t *testing.T) {
	client := setupTestRedis(t)
	defer client.FlushDB(context.Background())

	redisKey := "pdfnest:idempotency:0.0.0.0:race-key-777"
	initialRecord := Record{
		State:         "PROCESSING",
		Fingerprint:   "fp123",
		CreatedAt:     1000,
		OwnerIdentity: "0.0.0.0",
	}
	data, _ := json.Marshal(initialRecord)
	_ = client.Set(context.Background(), redisKey, string(data), ProcessingTTL).Err()

	app := fiber.New()
	app.Post("/test-race", func(c *fiber.Ctx) error {
		c.Locals("idempotency_redis_key", redisKey)
		c.Locals("idempotency_fingerprint", "fp123")
		c.Locals("idempotency_owner", "0.0.0.0")

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			_ = SetTaskID(c, "task-race-111", client)
		}()

		go func() {
			defer wg.Done()
			Release(c, client)
		}()

		wg.Wait()
		return c.SendStatus(200)
	})

	req := httptest.NewRequest("POST", "/test-race", nil)
	_, _ = app.Test(req)

	val, err := client.Get(context.Background(), redisKey).Result()
	// Final state MUST be either deleted (Release won first) OR CREATED (SetTaskID won)
	// It MUST NEVER be a deleted key IF state was already CREATED when Release executed!
	if err == nil && val != "" {
		if !bytes.Contains([]byte(val), []byte("CREATED")) {
			t.Errorf("Expected remaining record to be CREATED, got: %s", val)
		}
	}
}
