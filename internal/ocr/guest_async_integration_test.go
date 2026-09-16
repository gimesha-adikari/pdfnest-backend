package ocr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// This exercises the production request boundary locally: guest HTTP
// submission, real task creation, a controlled worker response, local
// artifact persistence, guest billing finalization, and task completion.
func TestGuestAsyncRequestPostFixSuccess(t *testing.T) {
	db, client := setupGuestAsyncIntegration(t)
	if db == nil || client == nil {
		t.Skip("isolated local PostgreSQL/Redis are required")
	}

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ocr/extract-text" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("controlled guest fixture text\n"))
	}))
	defer worker.Close()
	t.Setenv("PDFNEST_WORKER_URL", worker.URL)

	app := fiber.New()
	guestID := uuid.NewString()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: guestID, QuotaID: guestID, Type: identity.TypeGuest})
		c.Locals(identity.LocalIdentityIDKey, guestID)
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalUserIDKey, guestID)
		return c.Next()
	})
	app.Post(
		"/api/ocr/extract-text-async",
		uploads.Prepare(),
		limiter.Default.Middleware(),
		idempotency.Use(nil),
		NewController(NewService()).HandleAsyncExtractText,
	)

	fixture := filepath.Join("..", "..", "..", "pdfnest", "tests", "fixtures", "normal_text.pdf")
	fixtureBytes, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read controlled fixture %s: %v", fixture, err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "normal_text.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(fixtureBytes); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("lang", "eng"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ocr/extract-text-async", &body)
	req.Header.Set(fiber.HeaderContentType, form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "guest-async-red-"+uuid.NewString())
	resp, err := app.Test(req, 10_000)
	if err != nil {
		t.Fatalf("guest async request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusAccepted {
		t.Fatalf("expected HTTP 202, got %d", resp.StatusCode)
	}

	var submitted struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.TaskID == "" {
		t.Fatal("expected task ID")
	}

	deadline := time.Now().Add(10 * time.Second)
	var task *tasks.TaskStatus
	for time.Now().Before(deadline) {
		task, err = tasks.Registry.Get(submitted.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task != nil && (task.Status == "COMPLETED" || task.Status == "FAILED" || task.Status == "CANCELLED") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if task == nil {
		t.Fatal("task never became visible")
	}
	if task.Status != "COMPLETED" {
		t.Fatalf("expected completed task, got status=%s error=%q", task.Status, task.Error)
	}
	if task.ReservationKind != string(billing.ReservationKindGuest) {
		t.Fatalf("expected guest reservation metadata, got %q", task.ReservationKind)
	}
	artifact, err := os.ReadFile(task.ResultURL)
	if err != nil {
		t.Fatalf("read local artifact %s: %v", task.ResultURL, err)
	}
	if len(artifact) == 0 {
		t.Fatal("expected non-empty artifact")
	}
	if string(artifact) != "controlled guest fixture text\n" {
		t.Fatalf("unexpected artifact content %q", string(artifact))
	}
	digest := sha256.Sum256(artifact)
	t.Logf("artifact_bytes=%d artifact_sha256=%s", len(artifact), hex.EncodeToString(digest[:]))

	state, err := client.HGetAll(t.Context(), "platen:guestquota:state:"+guestID).Result()
	if err != nil {
		t.Fatal(err)
	}
	if state["pending_3h"] != "0" || state["used_3h"] == "0" {
		t.Fatalf("unexpected guest quota state after completion: %#v", state)
	}
	if exists, err := client.Exists(t.Context(), "platen:guestquota:res:"+task.ReservationID).Result(); err != nil || exists != 0 {
		t.Fatalf("expected finalization to remove reservation, exists=%d err=%v", exists, err)
	}
}

func TestGuestAsyncRequestWorkerFailureReleasesQuota(t *testing.T) {
	db, client := setupGuestAsyncIntegration(t)
	if db == nil || client == nil {
		t.Skip("isolated local PostgreSQL/Redis are required")
	}

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ocr/extract-text" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "controlled worker failure", http.StatusBadGateway)
	}))
	defer worker.Close()
	t.Setenv("PDFNEST_WORKER_URL", worker.URL)

	guestID := uuid.NewString()
	app := newGuestAsyncTestApp(guestID)
	taskID := submitGuestAsyncTestTask(t, app)
	task := waitForGuestAsyncTerminalTask(t, taskID)
	if task.Status != "FAILED" {
		t.Fatalf("expected failed task after worker failure, got status=%s error=%q", task.Status, task.Error)
	}
	if task.Error == "" {
		t.Fatal("expected worker failure to be recorded")
	}

	state, err := client.HGetAll(t.Context(), "platen:guestquota:state:"+guestID).Result()
	if err != nil {
		t.Fatal(err)
	}
	if state["pending_3h"] != "0" || state["used_3h"] != "0" {
		t.Fatalf("worker failure did not release guest quota: %#v", state)
	}
	if exists, err := client.Exists(t.Context(), "platen:guestquota:res:"+task.ReservationID).Result(); err != nil || exists != 0 {
		t.Fatalf("expected failed reservation to be removed, exists=%d err=%v", exists, err)
	}
}

func TestGuestAsyncRequestCancellationReleasesQuota(t *testing.T) {
	db, client := setupGuestAsyncIntegration(t)
	if db == nil || client == nil {
		t.Skip("isolated local PostgreSQL/Redis are required")
	}

	workerStarted := make(chan struct{}, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ocr/extract-text" {
			http.NotFound(w, r)
			return
		}
		select {
		case workerStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer worker.Close()
	t.Setenv("PDFNEST_WORKER_URL", worker.URL)

	guestID := uuid.NewString()
	app := newGuestAsyncTestApp(guestID)
	taskID := submitGuestAsyncTestTask(t, app)
	select {
	case <-workerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not receive the controlled cancellation fixture")
	}

	result, task, err := tasks.Registry.CancelTask(taskID, guestID)
	if err != nil {
		t.Fatalf("cancel guest task: %v", err)
	}
	if result != "CANCELLED_SUCCESS" || task == nil || task.Status != "CANCELLED" {
		t.Fatalf("expected successful task cancellation, result=%s task=%+v", result, task)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := client.HGetAll(t.Context(), "platen:guestquota:state:"+guestID).Result()
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		exists, existsErr := client.Exists(t.Context(), "platen:guestquota:res:"+task.ReservationID).Result()
		if existsErr != nil {
			t.Fatal(existsErr)
		}
		if state["pending_3h"] == "0" && state["used_3h"] == "0" && exists == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cancelled guest task retained billing state: reservation=%s", task.ReservationID)
}

func newGuestAsyncTestApp(guestID string) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: guestID, QuotaID: guestID, Type: identity.TypeGuest})
		c.Locals(identity.LocalIdentityIDKey, guestID)
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalUserIDKey, guestID)
		return c.Next()
	})
	app.Post(
		"/api/ocr/extract-text-async",
		uploads.Prepare(),
		limiter.Default.Middleware(),
		idempotency.Use(nil),
		NewController(NewService()).HandleAsyncExtractText,
	)
	return app
}

func submitGuestAsyncTestTask(t *testing.T, app *fiber.App) string {
	t.Helper()
	fixture := filepath.Join("..", "..", "..", "pdfnest", "tests", "fixtures", "normal_text.pdf")
	fixtureBytes, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read controlled fixture %s: %v", fixture, err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "normal_text.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(fixtureBytes); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("lang", "eng"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ocr/extract-text-async", &body)
	req.Header.Set(fiber.HeaderContentType, form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "guest-async-"+uuid.NewString())
	resp, err := app.Test(req, 10_000)
	if err != nil {
		t.Fatalf("guest async request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusAccepted {
		t.Fatalf("expected HTTP 202, got %d", resp.StatusCode)
	}

	var submitted struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.TaskID == "" {
		t.Fatal("expected task ID")
	}
	return submitted.TaskID
}

func waitForGuestAsyncTerminalTask(t *testing.T, taskID string) *tasks.TaskStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := tasks.Registry.Get(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task != nil && (task.Status == "COMPLETED" || task.Status == "FAILED" || task.Status == "CANCELLED") {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s never became terminal", taskID)
	return nil
}

func setupGuestAsyncIntegration(t *testing.T) (*gorm.DB, *redis.Client) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL")
	if dsn == "" || redisURL == "" {
		return nil, nil
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open isolated postgres: %v", err)
	}
	if err := db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{}); err != nil {
		t.Fatalf("migrate isolated postgres: %v", err)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse isolated redis URL: %v", err)
	}
	client := redis.NewClient(options)
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("reset isolated redis DB: %v", err)
	}

	oldDB, oldRedis, oldGuest, oldLimiter := config.DB, config.Redis, billing.GuestQuota, limiter.Default
	config.DB = db
	config.Redis = client
	billing.Initialize(billing.NewGuestQuotaStore(client, time.Hour))
	limiter.Default = limiter.NewGovernorWithCapacity(1)
	t.Cleanup(func() {
		config.DB = oldDB
		config.Redis = oldRedis
		billing.Initialize(oldGuest)
		limiter.Default = oldLimiter
		_ = client.Close()
	})
	return db, client
}
