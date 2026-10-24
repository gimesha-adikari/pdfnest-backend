package conversion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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
	var dedupScopes []string
	mockWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/render/sessions" && r.Method == http.MethodPost {
			createCount++
			dedupScopes = append(dedupScopes, r.URL.Query().Get("dedup_scope"))
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
	t.Setenv("WORKER_SHARED_SECRET", "synthetic-preview-worker-secret")

	svc := &ConversionService{}
	ctx := context.Background()

	tmpFile, err := os.CreateTemp("", "dummy-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString("%PDF-1.4 mock")
	_ = tmpFile.Close()

	res1, err := svc.createWorkerPreviewSession(ctx, mockWorker.URL, tmpFile.Name(), "guest:a")
	if err != nil {
		t.Fatalf("createWorkerPreviewSession failed: %v", err)
	}
	if res1.ID != "worker-sess-1" {
		t.Fatalf("Expected session worker-sess-1, got %s", res1.ID)
	}

	res2, err := svc.createWorkerPreviewSession(ctx, mockWorker.URL, tmpFile.Name(), "guest:b")
	if err != nil {
		t.Fatalf("createWorkerPreviewSession failed: %v", err)
	}
	if res2.ID != "worker-sess-2" {
		t.Fatalf("Expected session worker-sess-2, got %s", res2.ID)
	}
	if createCount != 2 {
		t.Fatalf("Expected worker to be called 2 times, got %d", createCount)
	}
	if len(dedupScopes) != 2 || dedupScopes[0] != previewWorkerDedupScope("guest:a") || dedupScopes[1] != previewWorkerDedupScope("guest:b") {
		t.Fatalf("worker received unexpected owner deduplication scopes: %v", dedupScopes)
	}
	if dedupScopes[0] == dedupScopes[1] {
		t.Fatal("worker received the same deduplication scope for different identities")
	}
}

func TestPreviewSessionService_IsolatesIdenticalUploadsByOwner(t *testing.T) {
	t.Setenv("WORKER_SHARED_SECRET", "synthetic-preview-worker-secret")

	previousCache := globalPreviewSessions
	globalPreviewSessions = &previewSessionCache{
		sessions: make(map[string]*previewWorkerSession),
		byHash:   make(map[string]string),
	}
	t.Cleanup(func() { globalPreviewSessions = previousCache })

	workerSessions := make(map[string][]byte)
	scopeHashSessions := make(map[string]string)
	mockWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/render/sessions":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, "invalid upload", http.StatusBadRequest)
				return
			}
			files := r.MultipartForm.File["file"]
			if len(files) != 1 {
				http.Error(w, "expected one file", http.StatusBadRequest)
				return
			}
			file, err := files[0].Open()
			if err != nil {
				http.Error(w, "could not open upload", http.StatusBadRequest)
				return
			}
			data, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil {
				http.Error(w, "could not read upload", http.StatusBadRequest)
				return
			}
			scope := r.URL.Query().Get("dedup_scope")
			if len(scope) != 64 || strings.Contains(scope, "guest") {
				http.Error(w, "invalid owner scope", http.StatusBadRequest)
				return
			}
			digest := sha256.Sum256(data)
			key := scope + "\x00" + hex.EncodeToString(digest[:])
			sessionID, ok := scopeHashSessions[key]
			if !ok {
				sessionID = fmt.Sprintf("worker-session-%d", len(workerSessions)+1)
				scopeHashSessions[key] = sessionID
				workerSessions[sessionID] = append([]byte(nil), data...)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": sessionID,
				"page_count": 2,
				"file_size":  len(data),
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/render/sessions/"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) != 7 || parts[3] != "sessions" || parts[5] != "page" {
				http.NotFound(w, r)
				return
			}
			if _, ok := workerSessions[parts[4]]; !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(createTestJpegBytes())
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/render/sessions/"):
			sessionID := strings.TrimPrefix(r.URL.Path, "/api/v1/render/sessions/")
			if _, ok := workerSessions[sessionID]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(workerSessions, sessionID)
			for key, id := range scopeHashSessions {
				if id == sessionID {
					delete(scopeHashSessions, key)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockWorker.Close()
	t.Setenv("PDFNEST_WORKER_URL", mockWorker.URL)

	fileHeader := func(name string) *multipart.FileHeader {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("%PDF-1.4 synthetic same-content preview")); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		return req.MultipartForm.File["file"][0]
	}

	svc := &ConversionService{}
	ctx := context.Background()
	create := func(ownerID, name string) string {
		t.Helper()
		result, err := svc.CreatePreviewSession(ctx, fileHeader(name), ownerID)
		if err != nil {
			t.Fatalf("CreatePreviewSession(%s) failed: %v", ownerID, err)
		}
		id, ok := result["session_id"].(string)
		if !ok || id == "" {
			t.Fatalf("CreatePreviewSession(%s) returned no session ID", ownerID)
		}
		return id
	}

	guestA := "guest:preview-owner-a"
	guestB := "guest:preview-owner-b"
	sessionA := create(guestA, "same-a.pdf")
	sessionAAgain := create(guestA, "same-a-again.pdf")
	sessionB := create(guestB, "same-b.pdf")
	if sessionA != sessionAAgain {
		t.Fatal("repeated identical upload from the same identity did not reuse its worker session")
	}
	if sessionA == sessionB {
		t.Fatal("different identities received a shared worker session ID")
	}
	if string(workerSessions[sessionA]) != "%PDF-1.4 synthetic same-content preview" || string(workerSessions[sessionB]) != string(workerSessions[sessionA]) {
		t.Fatal("mock worker did not receive the same expected synthetic PDF bytes for both identities")
	}

	for _, test := range []struct {
		ownerID   string
		sessionID string
	}{
		{ownerID: guestA, sessionID: sessionA},
		{ownerID: guestB, sessionID: sessionB},
	} {
		if _, err := svc.GetPreviewSessionPage(ctx, test.sessionID, 1, 2, test.ownerID); err != nil {
			t.Fatalf("owner page retrieval failed: %v", err)
		}
	}
	if _, err := svc.GetPreviewSessionPage(ctx, sessionA, 1, 2, guestB); err != ErrPreviewSessionForbidden {
		t.Fatalf("cross-identity page access error = %v, want forbidden", err)
	}
	if err := svc.DeletePreviewSession(ctx, sessionA, guestB); err != ErrPreviewSessionForbidden {
		t.Fatalf("cross-identity deletion error = %v, want forbidden", err)
	}
	if err := svc.DeletePreviewSession(ctx, sessionA, guestA); err != nil {
		t.Fatalf("guest A session deletion failed: %v", err)
	}
	if _, err := svc.GetPreviewSessionPage(ctx, sessionB, 1, 2, guestB); err != nil {
		t.Fatalf("deleting guest A session invalidated guest B session: %v", err)
	}
	if err := svc.DeletePreviewSession(ctx, sessionB, guestB); err != nil {
		t.Fatalf("guest B session deletion failed: %v", err)
	}
	if len(workerSessions) != 0 {
		t.Fatalf("worker sessions remained after owner deletion: %d", len(workerSessions))
	}
}

func TestCreatePreviewSessionFailureDoesNotPublishOrLeaveUploadTemp(t *testing.T) {
	t.Setenv("WORKER_SHARED_SECRET", "synthetic-preview-worker-secret")
	t.Setenv("TMPDIR", t.TempDir())

	previousCache := globalPreviewSessions
	globalPreviewSessions = &previewSessionCache{
		sessions: make(map[string]*previewWorkerSession),
		byHash:   make(map[string]string),
	}
	t.Cleanup(func() { globalPreviewSessions = previousCache })

	workerCalls := 0
	mockWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		if r.URL.Path != "/api/v1/render/sessions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockWorker.Close()
	t.Setenv("PDFNEST_WORKER_URL", mockWorker.URL)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "failure.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("%PDF-1.4 synthetic backend cleanup")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	fileHeader := req.MultipartForm.File["file"][0]

	svc := &ConversionService{}
	if _, err := svc.CreatePreviewSession(context.Background(), fileHeader, "guest:failure"); err == nil {
		t.Fatal("worker HTTP 500 unexpectedly published a preview session")
	}
	if workerCalls != 1 {
		t.Fatalf("worker request count = %d, want 1", workerCalls)
	}
	if len(globalPreviewSessions.sessions) != 0 || len(globalPreviewSessions.byHash) != 0 {
		t.Fatal("failed worker creation left a backend preview session published")
	}
	entries, err := os.ReadDir(os.Getenv("TMPDIR"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed worker creation left %d backend upload temporary files", len(entries))
	}

	if _, err := svc.CreatePreviewSession(context.Background(), fileHeader, "  "); err == nil {
		t.Fatal("preview session creation accepted a missing identity")
	}
	if workerCalls != 1 {
		t.Fatalf("missing identity reached the worker; request count = %d", workerCalls)
	}
}
