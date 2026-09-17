package conversion

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"pdfnest-backend/internal/identity"
)

func createTestJpegBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for x := 0; x < 10; x++ {
		for y := 0; y < 10; y++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func TestPreviewSessionRemediation_WorkerErrorMapping(t *testing.T) {
	mockWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/render/sessions/expired_session/page/1":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"detail": map[string]any{
					"code":    "SESSION_NOT_FOUND",
					"message": "Render session 'expired_session' was not found",
				},
			})
		case "/api/v1/render/sessions/valid_session/page/999":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"detail": map[string]any{
					"code":    "INVALID_RENDER_REQUEST",
					"message": "Page number 999 out of range",
				},
			})
		case "/api/v1/render/sessions/valid_session/page/1":
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(createTestJpegBytes())
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer mockWorker.Close()

	origURL := os.Getenv("PDFNEST_WORKER_URL")
	_ = os.Setenv("PDFNEST_WORKER_URL", mockWorker.URL)
	defer func() {
		if origURL != "" {
			_ = os.Setenv("PDFNEST_WORKER_URL", origURL)
		} else {
			_ = os.Unsetenv("PDFNEST_WORKER_URL")
		}
	}()

	ownerID := "guest:test-editor001"
	globalPreviewSessions.put(&previewWorkerSession{
		ID:           "expired_session",
		OwnerID:      ownerID,
		SourceHash:   "hash-expired",
		PageCount:    3,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	})
	globalPreviewSessions.put(&previewWorkerSession{
		ID:           "valid_session",
		OwnerID:      ownerID,
		SourceHash:   "hash-valid",
		PageCount:    3,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	})

	svc := &ConversionService{}
	ctrl := &Controller{service: svc}

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, "guest:test-editor001")
		return c.Next()
	})
	app.Get("/conversion/preview/session/:sessionId/page/:page", ctrl.StreamPreviewSessionPageHandler)

	t.Run("Worker_404_Yields_HTTP_404_SESSION_NOT_FOUND_Not_500", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/conversion/preview/session/expired_session/page/1", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test failed: %v", err)
		}

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Expected HTTP 404 for expired worker session, got status %d", resp.StatusCode)
		}

		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body["code"] != "SESSION_NOT_FOUND" {
			t.Fatalf("Expected error code 'SESSION_NOT_FOUND', got %v", body["code"])
		}

		if _, ok := globalPreviewSessions.sessions["expired_session"]; ok {
			t.Fatalf("Expected expired session to be purged from cache on 404")
		}
	})

	t.Run("Worker_400_Yields_HTTP_400_INVALID_PAGE_Not_500", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/conversion/preview/session/valid_session/page/999", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test failed: %v", err)
		}

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("Expected HTTP 400 for out-of-range worker page, got status %d", resp.StatusCode)
		}

		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body["code"] != "INVALID_PAGE" {
			t.Fatalf("Expected error code 'INVALID_PAGE', got %v", body["code"])
		}
	})

	t.Run("Worker_200_Yields_HTTP_200_JPEG", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/conversion/preview/session/valid_session/page/1", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test failed: %v", err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected HTTP 200 for valid page, got status %d", resp.StatusCode)
		}
		if resp.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("Expected Content-Type image/jpeg, got %s", resp.Header.Get("Content-Type"))
		}
	})

	t.Run("Direct_renderWorkerSessionPage_ErrorTypes", func(t *testing.T) {
		ctx := context.Background()

		_, err := svc.renderWorkerSessionPage(ctx, mockWorker.URL, "expired_session", 1, 144.0)
		if err != ErrPreviewSessionNotFound {
			t.Fatalf("Expected ErrPreviewSessionNotFound, got %v", err)
		}

		_, err = svc.renderWorkerSessionPage(ctx, mockWorker.URL, "valid_session", 999, 144.0)
		if err != ErrPreviewInvalidPage {
			t.Fatalf("Expected ErrPreviewInvalidPage, got %v", err)
		}
	})
}

func TestCreatePreviewSession_DelegatesToWorkerAlways(t *testing.T) {
	createCount := 0
	mockWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/render/sessions" && r.Method == http.MethodPost {
			createCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": "worker-sess-" + strconv.Itoa(createCount),
				"page_count": 3,
				"file_size":  1234,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockWorker.Close()

	origURL := os.Getenv("PDFNEST_WORKER_URL")
	_ = os.Setenv("PDFNEST_WORKER_URL", mockWorker.URL)
	defer func() {
		if origURL != "" {
			_ = os.Setenv("PDFNEST_WORKER_URL", origURL)
		} else {
			_ = os.Unsetenv("PDFNEST_WORKER_URL")
		}
	}()

	svc := &ConversionService{}
	ctx := context.Background()

	tmpFile, err := os.CreateTemp("", "dummy-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString("%PDF-1.4 mock")
	_ = tmpFile.Close()

	res1, err := svc.createWorkerPreviewSession(ctx, mockWorker.URL, tmpFile.Name())
	if err != nil {
		t.Fatalf("createWorkerPreviewSession failed: %v", err)
	}
	if res1.ID != "worker-sess-1" {
		t.Fatalf("Expected session worker-sess-1, got %s", res1.ID)
	}

	res2, err := svc.createWorkerPreviewSession(ctx, mockWorker.URL, tmpFile.Name())
	if err != nil {
		t.Fatalf("createWorkerPreviewSession failed: %v", err)
	}
	if res2.ID != "worker-sess-2" {
		t.Fatalf("Expected session worker-sess-2, got %s", res2.ID)
	}
	if createCount != 2 {
		t.Fatalf("Expected worker to be called 2 times, got %d", createCount)
	}
}
