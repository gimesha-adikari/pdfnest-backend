package conversion

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"pdfnest-backend/internal/uploads"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestPreviewSessionHandlers_Validation(t *testing.T) {
	app := fiber.New()
	ctrl := &Controller{service: nil}

	app.Post("/conversion/preview/session", uploads.Prepare(), ctrl.CreatePreviewSessionHandler)
	app.Get("/conversion/preview/session/:sessionId/page/:page", ctrl.StreamPreviewSessionPageHandler)
	app.Delete("/conversion/preview/session/:sessionId", ctrl.DeletePreviewSessionHandler)

	t.Run("CreatePreviewSessionHandler_MissingFile", func(t *testing.T) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		_ = w.Close()

		req := httptest.NewRequest("POST", "/conversion/preview/session", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("App.Test failed: %v", err)
		}

		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("Expected status 400 for missing file, got %d", resp.StatusCode)
		}
	})

	t.Run("StreamPreviewSessionPageHandler_InvalidPage", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/conversion/preview/session/sess_123/page/0", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("App.Test failed: %v", err)
		}

		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("Expected status 400 for invalid page 0, got %d", resp.StatusCode)
		}
	})

	t.Run("DeletePreviewSessionHandler_MissingSessionID", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/conversion/preview/session/", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("App.Test failed: %v", err)
		}

		// Fiber router won't match route with empty param, returns 404 or 400
		if resp.StatusCode != fiber.StatusNotFound && resp.StatusCode != fiber.StatusBadRequest && resp.StatusCode != fiber.StatusMethodNotAllowed {
			t.Errorf("Expected status 404, 400 or 405, got %d", resp.StatusCode)
		}
	})
}
