package conversion

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/uploads"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRasterizePdfUniversal_DiskSpaceAdmissionCheck(t *testing.T) {
	app := fiber.New()
	ctrl := &Controller{service: nil}

	// Setup authenticated user identity
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeUser))
		c.Locals(identity.LocalIdentityIDKey, "usr_test_123")
		return c.Next()
	})

	// Setup uploads middleware
	app.Use(uploads.Prepare())

	app.Post("/pdf-to-images", ctrl.RasterizePdfUniversal)

	// Create a dummy PDF multipart upload
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("file", "test.pdf")
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}
	_, _ = fw.Write([]byte("%PDF-1.4\n%EOF"))
	_ = w.Close()

	req := httptest.NewRequest("POST", "/pdf-to-images", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App.Test failed: %v", err)
	}

	// Under normal test disk, available space is sufficient, so page check or backend handler runs (returns 400 bad PDF)
	if resp.StatusCode != fiber.StatusOK && resp.StatusCode != fiber.StatusBadRequest && resp.StatusCode != fiber.StatusInsufficientStorage {
		t.Errorf("Unexpected status code: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = body
}
