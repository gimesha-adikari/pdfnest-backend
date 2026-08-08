package uploads

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"pdfnest-backend/internal/temp"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestUploadMiddleware_AllocatesInsideDedicatedTempDir(t *testing.T) {
	app := fiber.New()

	var stagedPath string
	app.Post("/upload", Prepare(), func(c *fiber.Ctx) error {
		ctx := FromCtx(c)
		if file, ok := ctx.First("file"); ok {
			stagedPath = file.Path
		}
		return c.SendString("ok")
	})

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	// User attempts path traversal in filename: ../../malicious.pdf
	fw, err := w.CreateFormFile("file", "../../malicious.pdf")
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}
	_, _ = fw.Write([]byte("dummy content"))
	_ = w.Close()

	req := httptest.NewRequest("POST", "/upload", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("App.Test failed: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode)
	}

	if stagedPath == "" {
		t.Fatal("Expected staged file path to be populated")
	}

	dedicatedDir := temp.GetDir()
	stagedDir := filepath.Dir(stagedPath)

	if stagedDir != dedicatedDir {
		t.Errorf("Expected upload staged directory %s, got %s", dedicatedDir, stagedDir)
	}

	filename := filepath.Base(stagedPath)
	if !strings.HasPrefix(filename, "pdfnest-upload-") {
		t.Errorf("Expected filename prefix pdfnest-upload-, got %s", filename)
	}

	// Path traversal protection assertion: filename must NOT contain '..' or directory separators
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		t.Errorf("CRITICAL SECURITY DEFECT: Upload path traversal escape detected in %s", filename)
	}
}
