package worker

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGetWorkerURL(t *testing.T) {
	os.Unsetenv("PDFNEST_WORKER_URL")
	if url := GetWorkerURL(); url != "http://localhost:8000" {
		t.Errorf("expected default http://localhost:8000, got %s", url)
	}

	os.Setenv("PDFNEST_WORKER_URL", "http://custom-worker:9000/")
	defer os.Unsetenv("PDFNEST_WORKER_URL")

	if url := GetWorkerURL(); url != "http://custom-worker:9000" {
		t.Errorf("expected http://custom-worker:9000, got %s", url)
	}
}

func TestSignRequest(t *testing.T) {
	req, err := http.NewRequest("POST", "http://worker/api/v1/office/convert?format=docx", nil)
	if err != nil {
		t.Fatalf("failed to create req: %v", err)
	}

	secret := "test-hmac-secret-123"
	if err := SignRequestWithSecret(req, secret); err != nil {
		t.Fatalf("SignRequestWithSecret failed: %v", err)
	}

	sig := req.Header.Get("X-Worker-Signature")
	ts := req.Header.Get("X-Worker-Timestamp")
	nonce := req.Header.Get("X-Worker-Nonce")

	if sig == "" || ts == "" || nonce == "" {
		t.Errorf("expected signature headers to be set")
	}

	stringToSign := "POST\n/api/v1/office/convert?format=docx\n" + ts + "\n" + nonce
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if sig != expectedSig {
		t.Errorf("expected signature %s, got %s", expectedSig, sig)
	}
}

func TestCreateMultipartRequest_Streaming(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test-pdf-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

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

	resp, err := Client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if !handlerCalled {
		t.Errorf("expected handler to be called")
	}
}
