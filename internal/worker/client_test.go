package worker

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestWorkerAuthenticationContract(t *testing.T) {
	secret := "secret-auth-test-xyz"
	t.Setenv("WORKER_SHARED_SECRET", secret)

	seenNonces := make(map[string]bool)
	var nonceMu sync.Mutex

	// Mock server that verifies HMAC-SHA256 headers and prevents replay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.Header.Get("X-Worker-Signature")
		ts := r.Header.Get("X-Worker-Timestamp")
		nonce := r.Header.Get("X-Worker-Nonce")

		if sig == "" || ts == "" || nonce == "" {
			http.Error(w, "missing auth headers", http.StatusUnauthorized)
			return
		}

		tsInt, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || abs(time.Now().Unix()-tsInt) > 300 {
			http.Error(w, "expired timestamp", http.StatusUnauthorized)
			return
		}

		nonceMu.Lock()
		if seenNonces[nonce] {
			nonceMu.Unlock()
			http.Error(w, "nonce replayed", http.StatusUnauthorized)
			return
		}
		seenNonces[nonce] = true
		nonceMu.Unlock()

		stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s", r.Method, r.URL.Path, ts, nonce)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(stringToSign))
		expectedSig := hex.EncodeToString(mac.Sum(nil))

		if sig != expectedSig {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"authenticated":true}`))
	}))
	defer server.Close()

	// 1. Valid request via Client.Do -> must succeed
	reqValid, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", bytes.NewReader([]byte("data")))
	respValid, err := Client.Do(reqValid)
	if err != nil {
		t.Fatalf("Valid request failed: %v", err)
	}
	defer respValid.Body.Close()
	if respValid.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 for valid signed request, got %d", respValid.StatusCode)
	}

	// 2. Direct request without headers -> must return 401
	directClient := &http.Client{}
	reqMissing, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", nil)
	respMissing, err := directClient.Do(reqMissing)
	if err != nil {
		t.Fatalf("Direct request failed: %v", err)
	}
	defer respMissing.Body.Close()
	if respMissing.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for missing headers, got %d", respMissing.StatusCode)
	}

	// 3. Direct request with invalid signature -> must return 401
	reqInvalid, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", nil)
	reqInvalid.Header.Set("X-Worker-Signature", "invalid_signature_hex")
	reqInvalid.Header.Set("X-Worker-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	reqInvalid.Header.Set("X-Worker-Nonce", "nonce-12345")
	respInvalid, err := directClient.Do(reqInvalid)
	if err != nil {
		t.Fatalf("Direct request failed: %v", err)
	}
	defer respInvalid.Body.Close()
	if respInvalid.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for invalid signature, got %d", respInvalid.StatusCode)
	}

	// 4. Direct request with expired timestamp -> must return 401
	reqExpired, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", nil)
	expiredTs := strconv.FormatInt(time.Now().Unix()-600, 10)
	nonceExpired := "nonce-expired-123"
	stringToSign := fmt.Sprintf("POST\n/api/v1/render/page\n%s\n%s", expiredTs, nonceExpired)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	reqExpired.Header.Set("X-Worker-Signature", hex.EncodeToString(mac.Sum(nil)))
	reqExpired.Header.Set("X-Worker-Timestamp", expiredTs)
	reqExpired.Header.Set("X-Worker-Nonce", nonceExpired)
	respExpired, err := directClient.Do(reqExpired)
	if err != nil {
		t.Fatalf("Direct request failed: %v", err)
	}
	defer respExpired.Body.Close()
	if respExpired.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for expired timestamp, got %d", respExpired.StatusCode)
	}

	// 5. Replayed nonce -> first succeeds, second fails with 401
	replayNonce := "nonce-replay-unique-456"
	currentTs := strconv.FormatInt(time.Now().Unix(), 10)
	strToSignReplay := fmt.Sprintf("POST\n/api/v1/render/page\n%s\n%s", currentTs, replayNonce)
	macReplay := hmac.New(sha256.New, []byte(secret))
	macReplay.Write([]byte(strToSignReplay))
	sigReplay := hex.EncodeToString(macReplay.Sum(nil))

	reqReplay1, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", nil)
	reqReplay1.Header.Set("X-Worker-Signature", sigReplay)
	reqReplay1.Header.Set("X-Worker-Timestamp", currentTs)
	reqReplay1.Header.Set("X-Worker-Nonce", replayNonce)
	respR1, err := directClient.Do(reqReplay1)
	require.NoError(t, err)
	defer respR1.Body.Close()
	assert.Equal(t, http.StatusOK, respR1.StatusCode, "First request with unique nonce must succeed")

	reqReplay2, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/render/page", nil)
	reqReplay2.Header.Set("X-Worker-Signature", sigReplay)
	reqReplay2.Header.Set("X-Worker-Timestamp", currentTs)
	reqReplay2.Header.Set("X-Worker-Nonce", replayNonce)
	respR2, err := directClient.Do(reqReplay2)
	require.NoError(t, err)
	defer respR2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respR2.StatusCode, "Replayed nonce must be rejected with 401")
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
