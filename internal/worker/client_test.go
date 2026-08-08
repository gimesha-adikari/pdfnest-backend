package worker

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGetWorkerURL(t *testing.T) {
	// Test fallback default
	os.Unsetenv("PDFNEST_WORKER_URL")
	if url := GetWorkerURL(); url != "http://localhost:8000" {
		t.Errorf("expected default http://localhost:8000, got %s", url)
	}

	// Test environment variable precedence
	os.Setenv("PDFNEST_WORKER_URL", "http://custom-worker:9000/")
	defer os.Unsetenv("PDFNEST_WORKER_URL")

	if url := GetWorkerURL(); url != "http://custom-worker:9000" {
		t.Errorf("expected http://custom-worker:9000, got %s", url)
	}
}

func TestCreateMultipartRequest_Streaming(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test-pdf-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Write test payload (100 KB)
	data := bytes.Repeat([]byte("PDF-DUMMY-DATA-"), 6400)
	if _, err := tempFile.Write(data); err != nil {
		t.Fatalf("failed to write dummy data: %v", err)
	}
	_ = tempFile.Close()

	reader, contentType, err := CreateMultipartRequest(tempFile.Name(), func(w *multipart.Writer) error {
		return w.WriteField("field_key", "field_val")
	})
	if err != nil {
		t.Fatalf("CreateMultipartRequest failed: %v", err)
	}

	if contentType == "" {
		t.Errorf("expected non-empty contentType")
	}

	// Test streaming payload directly to HTTP mock server
	handlerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true

		if err := r.ParseMultipartForm(10 * 1024 * 1024); err != nil {
			t.Fatalf("failed to parse multipart form: %v", err)
		}

		f, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("missing 'file' in form: %v", err)
		}
		defer f.Close()

		if header.Filename != filepath.Base(tempFile.Name()) {
			t.Errorf("expected filename %s, got %s", filepath.Base(tempFile.Name()), header.Filename)
		}

		val := r.FormValue("field_key")
		if val != "field_val" {
			t.Errorf("expected field_key to be field_val, got %s", val)
		}

		readBytes, _ := io.ReadAll(f)
		if len(readBytes) != len(data) {
			t.Errorf("expected %d bytes, got %d", len(data), len(readBytes))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL, reader)
	if err != nil {
		t.Fatalf("failed to build HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if !handlerCalled {
		t.Errorf("handler was not called")
	}
}
