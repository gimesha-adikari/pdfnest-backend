package storage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestLocalSearchablePDFUploadIsOwnerScopedAndUsesSharedStorage(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)

	controller := NewController()
	app := fiber.New(fiber.Config{BodyLimit: maxLocalOCRUploadBytes})
	app.Post("/api/storage/r2/presign", func(c *fiber.Ctx) error {
		c.Locals("user_id", c.Get("X-Test-User"))
		return controller.PresignUploads(c)
	})
	app.Put("/api/storage/local-ocr-image", func(c *fiber.Ctx) error {
		c.Locals("user_id", c.Get("X-Test-User"))
		return controller.UploadLocalOCRImage(c)
	})

	image := []byte("synthetic image fixture")
	requestBody := []byte(`{"purpose":"image_to_text_pdf","prefix":"client-supplied","files":[{"name":"synthetic.png","size":23,"type":"image/png","key":"client-key"}]}`)
	request := httptest.NewRequest("POST", "/api/storage/r2/presign", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-User", "user:alice")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusOK {
		t.Fatalf("presign status = %d", response.StatusCode)
	}
	var presigned PresignResponse
	if err := json.NewDecoder(response.Body).Decode(&presigned); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(presigned.Files) != 1 {
		t.Fatalf("presign returned %d files", len(presigned.Files))
	}
	item := presigned.Files[0]
	if !item.WithCredentials || item.Method != "PUT" || !IsOwnedKey(item.Key, "user:alice", "image_to_text_pdf") {
		t.Fatalf("unexpected local upload authorization: %+v", item)
	}

	put := func(user string) *httpResponse {
		uploadURL, parseErr := url.Parse(item.UploadURL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		req := httptest.NewRequest("PUT", uploadURL.RequestURI(), bytes.NewReader(image))
		req.Header.Set("Content-Type", "image/png")
		req.Header.Set("X-Test-User", user)
		req.ContentLength = int64(len(image))
		resp, callErr := app.Test(req)
		if callErr != nil {
			t.Fatal(callErr)
		}
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		_ = resp.Body.Close()
		return &httpResponse{status: resp.StatusCode, body: body}
	}

	if got := put("user:bob"); got.status != fiber.StatusForbidden {
		t.Fatalf("cross-owner upload status = %d, body=%s", got.status, got.body)
	}
	if got := put("user:alice"); got.status != fiber.StatusNoContent {
		t.Fatalf("owner upload status = %d, body=%s", got.status, got.body)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Key)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, image) {
		t.Fatalf("stored bytes = %q, want %q", stored, image)
	}
}

type httpResponse struct {
	status int
	body   []byte
}
