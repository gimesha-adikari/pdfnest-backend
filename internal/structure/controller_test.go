package structure

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/uploads"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type mockStructureService struct{}

func (m *mockStructureService) MergePDFs(inputPaths []string) (string, error) {
	for _, p := range inputPaths {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("input file missing at %s: %w", p, err)
		}
	}
	tmp := filepath.Join(os.TempDir(), "test_merged.pdf")
	_ = os.WriteFile(tmp, []byte("%PDF-1.4 dummy merged"), 0644)
	return tmp, nil
}

func (m *mockStructureService) SplitPDF(inputPath string, pageSelection []string) (string, error) {
	if _, err := os.Stat(inputPath); err != nil {
		return "", fmt.Errorf("input file missing at %s: %w", inputPath, err)
	}
	tmp := filepath.Join(os.TempDir(), "test_split.pdf")
	_ = os.WriteFile(tmp, []byte("%PDF-1.4 dummy split"), 0644)
	return tmp, nil
}

func (m *mockStructureService) RotatePDF(inputPath string, rotations map[string]int) (string, error) {
	return "", nil
}

func (m *mockStructureService) DeletePDFPages(inputPath string, pagesToDelete []string) (string, error) {
	return "", nil
}

func (m *mockStructureService) ReorderPDFPages(inputPath string, sequence []string) (string, error) {
	return "", nil
}

func (m *mockStructureService) WatermarkPDF(inputPath string, text string, imagePath string, description string) (string, error) {
	return "", nil
}

func (m *mockStructureService) WatermarkPDFOnPages(inputPath string, text string, imagePath string, description string, selectedPages []string) (string, error) {
	return "", nil
}

func (m *mockStructureService) AddPageNumbersPDF(inputPath string, description string) (string, error) {
	return "", nil
}

func (m *mockStructureService) UpdateMetadataPDF(inputPath string, metadata map[string]string, password string) (string, error) {
	return "", nil
}

func (m *mockStructureService) GetMetadataPDF(inputPath string, password string) (map[string]string, error) {
	return nil, nil
}

func (m *mockStructureService) CropPDF(inputPath string, cropBoxDesc string, selectedPages []string) (string, error) {
	if _, err := os.Stat(inputPath); err != nil {
		return "", fmt.Errorf("input file missing at %s: %w", inputPath, err)
	}
	tmp := filepath.Join(os.TempDir(), "test_cropped.pdf")
	_ = os.WriteFile(tmp, []byte("%PDF-1.4 dummy cropped"), 0644)
	return tmp, nil
}

func (m *mockStructureService) DuplicatePDFPages(inputPath string, pageSelection string, copies int) (string, error) {
	return "", nil
}

func (m *mockStructureService) InsertBlankPages(inputPath string, insertAt string, targetPage int, count int) (string, error) {
	return "", nil
}

func (m *mockStructureService) AddTextToPDF(inputPath string, elements []TextElement) (string, error) {
	return "", nil
}

func (m *mockStructureService) SignPDF(inputPath string, signaturePath string, outputPath string, stampsJSON string) error {
	return nil
}

func (m *mockStructureService) AnalyzePDF(inputPath, filePassword string) (*PDFAnalysis, error) {
	return &PDFAnalysis{PageCount: 1}, nil
}

var validPDFBytes = []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \ntrailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n190\n%%EOF")

func setupTestApp(t *testing.T) (*fiber.App, string) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skip("Postgres database connection unavailable, skipping test")
	}

	_ = db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{})
	config.DB = db
	billing.Default = billing.NewService()

	userID := uuid.New().String()
	user := config.User{ID: userID, Email: userID + "@example.com", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = db.Create(&user).Error

	subID := uuid.New().String()
	sub := config.Subscription{
		ID: subID, UserID: userID, PaddleCustomerID: "cus_" + subID, PaddleSubscriptionID: "sub_" + subID,
		Tier: "pro", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	_ = db.Create(&sub).Error

	app := fiber.New(fiber.Config{
		BodyLimit: 100 * 1024 * 1024,
	})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("identity_type", "user")
		c.Locals("identity_id", userID)
		c.Locals("user_id", userID)
		return c.Next()
	})
	ctrl := NewController(&mockStructureService{})
	RegisterRoutes(app, ctrl)
	return app, userID
}

func createMultipartRequest(t *testing.T, targetURL string, fieldName string, fileName string, content []byte, extraFields map[string]string) *http.Request {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("failed to write content: %v", err)
	}

	for k, v := range extraFields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("failed to write field %s: %v", k, err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}

	req := httptest.NewRequest("POST", targetURL, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestUploadContext_MustPDFFile(t *testing.T) {
	app := fiber.New()
	app.Post("/test-upload", uploads.Prepare(), func(c *fiber.Ctx) error {
		file, err := uploads.MustPDFFile(c, "file")
		if err != nil {
			return c.Status(fiber.StatusBadRequest).SendString(err.Error())
		}
		if file.Header.Filename != "sample.pdf" {
			return c.Status(fiber.StatusInternalServerError).SendString("filename mismatch")
		}
		if _, err := os.Stat(file.Path); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("staged file missing")
		}
		return c.SendString(file.Path)
	})

	req := createMultipartRequest(t, "/test-upload", "file", "sample.pdf", validPDFBytes, nil)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestSplit_SmallFile(t *testing.T) {
	app, _ := setupTestApp(t)

	req := createMultipartRequest(t, "/structure/split", "file", "small.pdf", validPDFBytes, map[string]string{
		"pages": "1",
	})

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestSplit_LargeDiskBackedFile(t *testing.T) {
	app, _ := setupTestApp(t)

	// Create a payload larger than 32MB to trigger fasthttp disk-backed temporary file creation (/tmp/multipart-*)
	largeSize := 33 * 1024 * 1024
	largeContent := make([]byte, largeSize)
	copy(largeContent, validPDFBytes)

	req := createMultipartRequest(t, "/structure/split", "file", "large_33mb.pdf", largeContent, map[string]string{
		"pages": "1",
	})

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200 for 33MB file split, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestMerge_LargeDiskBackedFiles(t *testing.T) {
	app, _ := setupTestApp(t)

	largeSize := 17 * 1024 * 1024 // Two 17MB files total >34MB
	file1 := make([]byte, largeSize)
	copy(file1, validPDFBytes)
	file2 := make([]byte, largeSize)
	copy(file2, validPDFBytes)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	p1, _ := writer.CreateFormFile("files", "doc1.pdf")
	_, _ = p1.Write(file1)
	p2, _ := writer.CreateFormFile("files", "doc2.pdf")
	_, _ = p2.Write(file2)

	_ = writer.Close()

	req := httptest.NewRequest("POST", "/structure/merge", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200 for 34MB total merge, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestParseSelectedPages(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"   ", nil},
		{"1", []string{"1"}},
		{"1, 2, 3", []string{"1", "2", "3"}},
		{`["1"]`, []string{"1"}},
		{`["1", "2"]`, []string{"1", "2"}},
		{`["1-3", "5"]`, []string{"1-3", "5"}},
	}

	for _, tt := range tests {
		result := parseSelectedPages(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseSelectedPages(%q) length mismatch: got %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("parseSelectedPages(%q)[%d] mismatch: got %s, want %s", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestCrop_BrowserPayload(t *testing.T) {
	app, _ := setupTestApp(t)

	// Test 1: Exact browser UI payload pages="[\"1\"]"
	req1 := createMultipartRequest(t, "/structure/crop", "file", "small.pdf", validPDFBytes, map[string]string{
		"box":   "[10 20 500 700]",
		"pages": `["1"]`,
	})
	resp1, err := app.Test(req1, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp1.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp1.Body)
		t.Fatalf("expected HTTP 200 for JSON pages payload, got %d: %s", resp1.StatusCode, string(respBody))
	}

	// Test 2: Comma-separated pages payload pages="1, 2"
	req2 := createMultipartRequest(t, "/structure/crop", "file", "small.pdf", validPDFBytes, map[string]string{
		"box":   "[10 20 500 700]",
		"pages": "1, 2",
	})
	resp2, err := app.Test(req2, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected HTTP 200 for string pages payload, got %d: %s", resp2.StatusCode, string(respBody))
	}
}
