package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/gofiber/fiber/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type localUploadHarness struct {
	app  *fiber.App
	ctrl *Controller
	root string
}

func localUploadTestApp(t *testing.T) localUploadHarness {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)
	ctrl := NewController()
	app := fiber.New(fiber.Config{BodyLimit: maxLocalOCRUploadBytes})
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", c.Get("X-Test-User")); return c.Next() })
	app.Post("/api/storage/r2/presign", ctrl.PresignUploads)
	app.Put("/api/storage/local-ocr-image", ctrl.UploadLocalOCRImage)
	return localUploadHarness{app, ctrl, root}
}
func imageFixture(t *testing.T, format string) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&out, img, nil)
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func (h localUploadHarness) presign(t *testing.T, body []byte, mediaType string) PresignItem {
	t.Helper()
	raw, err := json.Marshal(PresignRequest{Purpose: "image_to_text_pdf", Files: []PresignFile{{Name: "synthetic.png", Size: int64(len(body)), Type: mediaType, Key: "fabricated"}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/storage/r2/presign", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "user:alice")
	resp, err := h.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("presign=%d %s", resp.StatusCode, b)
	}
	var result PresignResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatal("expected one grant")
	}
	return result.Files[0]
}
func (h localUploadHarness) put(uploadURL, owner, mediaType string, body []byte) (int, error) {
	u, err := url.Parse(uploadURL)
	if err != nil {
		return 0, err
	}
	req := httptest.NewRequest("PUT", u.RequestURI(), bytes.NewReader(body))
	req.Header.Set("X-Test-User", owner)
	req.Header.Set("Content-Type", mediaType)
	resp, err := h.app.Test(req, -1)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
func requireUploadStatus(t *testing.T, h localUploadHarness, item PresignItem, owner, mediaType string, body []byte, want int) {
	t.Helper()
	got, err := h.put(item.UploadURL, owner, mediaType, body)
	if err != nil || got != want {
		t.Fatalf("status=%d err=%v want=%d", got, err, want)
	}
}
func TestLocalOCRGrantValidImagesAndReplay(t *testing.T) {
	h := localUploadTestApp(t)
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mime string
		body []byte
	}{{"image/png", imageFixture(t, "png")}, {"image/jpg", imageFixture(t, "jpeg")}, {"image/webp", webp}} {
		t.Run(tc.mime, func(t *testing.T) {
			item := h.presign(t, tc.body, tc.mime)
			if !item.WithCredentials || !IsOwnedKey(item.Key, "user:alice", "image_to_text_pdf") || item.Key == "fabricated" {
				t.Fatal("invalid issued target")
			}
			requireUploadStatus(t, h, item, "user:bob", tc.mime, tc.body, 403)
			requireUploadStatus(t, h, item, "user:alice", tc.mime, tc.body, 204)
			requireUploadStatus(t, h, item, "user:alice", tc.mime, tc.body, 409)
			path := filepath.Join(h.root, filepath.FromSlash(item.Key))
			stored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(stored, tc.body) {
				t.Fatalf("stored bytes differ: %v", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			requireUploadStatus(t, h, item, "user:alice", tc.mime, tc.body, 409)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("replay recreated removed object")
			}
		})
	}
}
func TestLocalOCRGrantForgeryExpiryAndBinding(t *testing.T) {
	h := localUploadTestApp(t)
	body := imageFixture(t, "png")
	item := h.presign(t, body, "image/png")
	for _, target := range []string{
		"/api/storage/local-ocr-image?key=" + url.QueryEscape(item.Key) + "&size=1",
		"/api/storage/local-ocr-image?key=" + url.QueryEscape(NewOwnedKey("user:alice", "image_to_text_pdf", ".png")) + "&size=1",
		"/api/storage/local-ocr-image?grant=fabricated.signature",
	} {
		bad := item
		bad.UploadURL = target
		requireUploadStatus(t, h, bad, "user:alice", "image/png", body, 403)
	}
	u, _ := url.Parse(item.UploadURL)
	parts := strings.Split(u.Query().Get("grant"), ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var claims map[string]interface{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"owner", "key", "purpose", "size", "media_type", "expires", "v"} {
		t.Run(field, func(t *testing.T) {
			changed := make(map[string]interface{})
			for k, v := range claims {
				changed[k] = v
			}
			changed[field] = "altered"
			raw, _ := json.Marshal(changed)
			q := u.Query()
			q.Set("grant", base64.RawURLEncoding.EncodeToString(raw)+"."+parts[1])
			v := *u
			v.RawQuery = q.Encode()
			bad := item
			bad.UploadURL = v.String()
			requireUploadStatus(t, h, bad, "user:alice", "image/png", body, 403)
		})
	}
	requireUploadStatus(t, h, item, "user:alice", "image/jpeg", body, 415)
	requireUploadStatus(t, h, item, "user:alice", "image/png", append(append([]byte(nil), body...), 0), 413)
	requireUploadStatus(t, h, item, "user:alice", "image/png", body[:len(body)-1], 413)
	h.ctrl.now = func() time.Time { return time.Now().Add(localOCRGrantLifetime + time.Second) }
	requireUploadStatus(t, h, item, "user:alice", "image/png", body, 403)
	h.ctrl.now = time.Now
	h.ctrl.uploadGrantKey = NewController().uploadGrantKey
	requireUploadStatus(t, h, item, "user:alice", "image/png", body, 403)
}
func TestLocalOCRGrantConcurrentPublicationAndExistingObject(t *testing.T) {
	h := localUploadTestApp(t)
	body := imageFixture(t, "png")
	item := h.presign(t, body, "image/png")
	start := make(chan struct{})
	statuses := make(chan int, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, e := h.put(item.UploadURL, "user:alice", "image/png", body)
			statuses <- s
			errs <- e
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	success, conflicts := 0, 0
	for s := range statuses {
		if s == 204 {
			success++
		} else if s == 409 {
			conflicts++
		} else {
			t.Fatalf("unexpected status %d", s)
		}
	}
	if success != 1 || conflicts != 7 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	stored, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(item.Key)))
	if err != nil || !bytes.Equal(stored, body) {
		t.Fatal("partial or altered publication")
	}
	item = h.presign(t, body, "image/png")
	target := filepath.Join(h.root, filepath.FromSlash(item.Key))
	if err := os.WriteFile(target, []byte("existing immutable input"), 0600); err != nil {
		t.Fatal(err)
	}
	requireUploadStatus(t, h, item, "user:alice", "image/png", body, 409)
	stored, _ = os.ReadFile(target)
	if string(stored) != "existing immutable input" {
		t.Fatal("existing input overwritten")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".ocr-upload-*"))
	if len(matches) != 0 {
		t.Fatal("staging leaked")
	}
}
func TestLocalOCRImageContentValidation(t *testing.T) {
	h := localUploadTestApp(t)
	pngBody := imageFixture(t, "png")
	huge := append([]byte(nil), pngBody...)
	binary.BigEndian.PutUint32(huge[16:20], 10001)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for _, tc := range []struct {
		name, mime string
		body       []byte
	}{
		{"fake PNG", "image/png", []byte("synthetic text is not PNG")}, {"corrupt PNG", "image/png", pngBody[:len(pngBody)/2]},
		{"wrong format", "image/png", imageFixture(t, "jpeg")}, {"unsafe dimensions", "image/png", huge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := h.presign(t, tc.body, tc.mime)
			requireUploadStatus(t, h, item, "user:alice", tc.mime, tc.body, 415)
			if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(item.Key))); !os.IsNotExist(err) {
				t.Fatal("invalid image published")
			}
		})
	}
}
func TestLocalOCRUploadEnvironmentGate(t *testing.T) {
	h := localUploadTestApp(t)
	body := imageFixture(t, "png")
	item := h.presign(t, body, "image/png")
	for _, env := range []string{"", "unknown", "dev", "preview", "canary", "staging", "production"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			requireUploadStatus(t, h, item, "user:alice", "image/png", body, 404)
			if localOCRUploadsEnabled() {
				t.Fatal("unexpected local grant activation")
			}
		})
	}
	for _, mode := range []string{"", "r2", "s3", "remote", "unknown"} {
		t.Run("mode-"+mode, func(t *testing.T) {
			t.Setenv("STORAGE_MODE", mode)
			requireUploadStatus(t, h, item, "user:alice", "image/png", body, 404)
		})
	}
	for _, env := range []string{"development", "test"} {
		t.Run("allowed-"+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			item := h.presign(t, body, "image/png")
			requireUploadStatus(t, h, item, "user:alice", "image/png", body, 204)
		})
	}
}

func TestLocalOCRGrantExpiresBeforePublication(t *testing.T) {
	h := localUploadTestApp(t)
	body := imageFixture(t, "png")
	item := h.presign(t, body, "image/png")
	start := time.Now()
	checks := 0
	h.ctrl.now = func() time.Time {
		checks++
		if checks >= 3 {
			return start.Add(localOCRGrantLifetime + time.Second)
		}
		return start
	}
	requireUploadStatus(t, h, item, "user:alice", "image/png", body, 403)
	if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(item.Key))); !os.IsNotExist(err) {
		t.Fatal("expired grant published")
	}
}

func TestLocalOCRPresignGatePreservesManagedAndExplicitRemoteAdapter(t *testing.T) {
	h := localUploadTestApp(t)
	// Signing only: no HTTP connection or production credentials are involved.
	client, err := minio.New("127.0.0.1:1", &minio.Options{Creds: credentials.NewStaticV4("unit-test", "unit-test", ""), Secure: false, Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	previousStore, previousErr := defaultStore, defaultErr
	once.Do(func() {})
	defaultStore, defaultErr = &Store{client: client, bucket: "synthetic-unit-test"}, nil
	t.Cleanup(func() {
		defaultStore, defaultErr = previousStore, previousErr
		once = sync.Once{}
		if previousStore != nil || previousErr != nil {
			once.Do(func() {})
		}
	})
	body := imageFixture(t, "png")
	for _, tc := range []struct{ env, mode string }{
		{"canary", "local"}, {"staging", "local"}, {"production", "local"},
		{"development", "r2"}, {"test", "remote"},
	} {
		t.Run(tc.env+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.env)
			t.Setenv("STORAGE_MODE", tc.mode)
			item := h.presign(t, body, "image/png")
			if item.WithCredentials || !strings.HasPrefix(item.UploadURL, "http://127.0.0.1:1/") || strings.Contains(item.UploadURL, "local-ocr-image") {
				t.Fatal("expected existing remote presign behavior")
			}
			if !strings.Contains(item.UploadURL, "X-Amz-Signature=") {
				t.Fatal("missing remote signature")
			}
		})
	}
}

func TestRepositoryAnalyzerPresignInLocalModeRequiresDirectUpload(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("R2_BUCKET", "synthetic-test-bucket")
	t.Setenv("R2_ACCESS_KEY", "synthetic-test-access")
	t.Setenv("R2_SECRET_KEY", "synthetic-test-secret")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")

	previousStore, previousErr := defaultStore, defaultErr
	defaultStore, defaultErr = nil, nil
	once = sync.Once{}
	t.Cleanup(func() {
		defaultStore, defaultErr = previousStore, previousErr
		once = sync.Once{}
		if previousStore != nil || previousErr != nil {
			once.Do(func() {})
		}
	})

	h := localUploadTestApp(t)
	reqBody, err := json.Marshal(PresignRequest{
		Purpose: "repository_analyzer",
		Files:   []PresignFile{{Name: "synthetic.zip", Size: 128, Type: "application/zip"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/storage/r2/presign", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "user:alice")
	resp, err := h.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusConflict {
		var apiError APIError
		_ = json.Unmarshal(responseBody, &apiError)
		t.Fatalf("local analyzer presign status=%d code=%s, want=%d", resp.StatusCode, apiError.Code, fiber.StatusConflict)
	}
	if strings.Contains(string(responseBody), "uploadUrl") || strings.Contains(string(responseBody), "127.0.0.1:1") {
		t.Fatal("local analyzer presign returned a remote upload destination")
	}
	var result APIError
	if err := json.Unmarshal(responseBody, &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != "LOCAL_ANALYZER_DIRECT_UPLOAD_REQUIRED" {
		t.Fatalf("unexpected local analyzer presign code: %q", result.Code)
	}
	if defaultStore != nil || defaultErr != nil {
		t.Fatal("local analyzer presign initialized the remote storage client")
	}
}

func TestFilesystemModeNeverFallsBackToRemotePresigns(t *testing.T) {
	// Deliberately provide a syntactically valid but unusable R2 endpoint. A
	// filesystem-mode request must be resolved or rejected before the remote
	// adapter is initialized, regardless of stale provider configuration.
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("R2_BUCKET", "synthetic-test-bucket")
	t.Setenv("R2_ACCESS_KEY", "synthetic-test-access")
	t.Setenv("R2_SECRET_KEY", "synthetic-test-secret")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")

	previousStore, previousErr := defaultStore, defaultErr
	defaultStore, defaultErr = nil, nil
	once = sync.Once{}
	t.Cleanup(func() {
		defaultStore, defaultErr = previousStore, previousErr
		once = sync.Once{}
		if previousStore != nil || previousErr != nil {
			once.Do(func() {})
		}
	})

	h := localUploadTestApp(t)
	fixture := imageFixture(t, "png")
	for _, tc := range []struct {
		name, appEnv, purpose, wantCode string
		wantLocalOCR                    bool
	}{
		{name: "analyzer requires its authenticated local upload route", appEnv: "development", purpose: "repository_analyzer", wantCode: "LOCAL_ANALYZER_DIRECT_UPLOAD_REQUIRED"},
		{name: "studio source cannot fall through to R2", appEnv: "development", purpose: "studio_source", wantCode: "LOCAL_UPLOAD_UNSUPPORTED_IN_FILESYSTEM_MODE"},
		{name: "OCR uses its dedicated local grant", appEnv: "development", purpose: "image_to_text_pdf", wantLocalOCR: true},
		{name: "OCR fails closed outside the local grant environment", appEnv: "", purpose: "image_to_text_pdf", wantCode: "LOCAL_UPLOAD_UNSUPPORTED_IN_FILESYSTEM_MODE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)
			defaultStore, defaultErr = nil, nil
			once = sync.Once{}

			body, err := json.Marshal(PresignRequest{
				Purpose: tc.purpose,
				Files:   []PresignFile{{Name: "synthetic-input", Size: int64(len(fixture)), Type: "image/png"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/storage/r2/presign", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "user:alice")
			resp, err := h.app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			responseBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			if tc.wantLocalOCR {
				var result PresignResponse
				if resp.StatusCode != fiber.StatusOK || json.Unmarshal(responseBody, &result) != nil || len(result.Files) != 1 {
					t.Fatalf("local OCR presign status=%d body=%s", resp.StatusCode, responseBody)
				}
				if !strings.Contains(result.Files[0].UploadURL, "/api/storage/local-ocr-image?") || strings.Contains(result.Files[0].UploadURL, "X-Amz-Signature=") {
					t.Fatalf("local OCR returned non-local destination %q", result.Files[0].UploadURL)
				}
			} else {
				var apiError APIError
				if resp.StatusCode != fiber.StatusConflict || json.Unmarshal(responseBody, &apiError) != nil || apiError.Code != tc.wantCode {
					t.Fatalf("presign status=%d code=%q body=%s want status=%d code=%q", resp.StatusCode, apiError.Code, responseBody, fiber.StatusConflict, tc.wantCode)
				}
				if strings.Contains(string(responseBody), "uploadUrl") || strings.Contains(string(responseBody), "127.0.0.1:1") || strings.Contains(string(responseBody), "X-Amz-Signature") {
					t.Fatalf("local presign returned remote destination: %s", responseBody)
				}
			}
			if defaultStore != nil || defaultErr != nil {
				t.Fatal("filesystem-mode presign initialized the remote storage adapter")
			}
		})
	}
}

func TestRepositoryAnalyzerPresignRetainsManagedRemoteMode(t *testing.T) {
	client, err := minio.New("127.0.0.1:1", &minio.Options{Creds: credentials.NewStaticV4("unit-test", "unit-test", ""), Secure: false, Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	previousStore, previousErr := defaultStore, defaultErr
	defaultStore, defaultErr = &Store{client: client, bucket: "synthetic-unit-test"}, nil
	once = sync.Once{}
	once.Do(func() {})
	t.Cleanup(func() {
		defaultStore, defaultErr = previousStore, previousErr
		once = sync.Once{}
		if previousStore != nil || previousErr != nil {
			once.Do(func() {})
		}
	})

	for _, tc := range []struct{ appEnv, storageMode string }{
		{appEnv: "development", storageMode: "r2"},
		{appEnv: "canary", storageMode: "local"},
		{appEnv: "staging", storageMode: "local"},
		{appEnv: "production", storageMode: "local"},
	} {
		t.Run(tc.appEnv+"/"+tc.storageMode, func(t *testing.T) {
			h := localUploadTestApp(t)
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("STORAGE_MODE", tc.storageMode)
			body, err := json.Marshal(PresignRequest{
				Purpose: "repository_analyzer",
				Files:   []PresignFile{{Name: "synthetic.zip", Size: 128, Type: "application/zip"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/storage/r2/presign", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "user:alice")
			resp, err := h.app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != fiber.StatusOK {
				responseBody, _ := io.ReadAll(resp.Body)
				t.Fatalf("remote analyzer presign status=%d: %s", resp.StatusCode, responseBody)
			}
			var result PresignResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if len(result.Files) != 1 || !strings.HasPrefix(result.Files[0].UploadURL, "http://127.0.0.1:1/") || !strings.Contains(result.Files[0].UploadURL, "X-Amz-Signature=") {
				t.Fatal("managed remote mode did not preserve the mocked presign contract")
			}
			if !IsOwnedKey(result.Files[0].Key, "user:alice", "repository_analyzer") {
				t.Fatal("remote analyzer key is not owner-scoped")
			}
		})
	}
}
