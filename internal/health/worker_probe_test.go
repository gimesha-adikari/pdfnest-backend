package health

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkerReadinessProbeUsesTheSignedWorkerContract(t *testing.T) {
	const secret = "synthetic-readiness-test-secret"
	t.Setenv("WORKER_SHARED_SECRET", secret)

	var authenticated bool
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read worker probe body: %v", err)
		}
		bodyHashBytes := sha256.Sum256(body)
		bodyHash := hex.EncodeToString(bodyHashBytes[:])
		timestamp := r.Header.Get("X-Worker-Timestamp")
		nonce := r.Header.Get("X-Worker-Nonce")
		signature := r.Header.Get("X-Worker-Signature")
		authenticated = r.Header.Get("X-Worker-Body-Hash") == bodyHash && timestamp != "" && nonce != ""
		canonical := fmt.Sprintf("GET\n%s\n%s\n%s\n%s", r.URL.RequestURI(), timestamp, nonce, bodyHash)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		authenticated = authenticated && hmac.Equal([]byte(strings.ToLower(signature)), []byte(hex.EncodeToString(mac.Sum(nil))))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not_ready","capabilities":{"document_processing":true,"ocr":false,"office_to_pdf":true,"pdf_to_office":true},"redis":true}`)
	}))
	defer server.Close()
	t.Setenv("PDFNEST_WORKER_URL", server.URL)

	status, capabilities, err := probeWorkerReadiness(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "/internal/health/ready", path)
	require.True(t, authenticated, "Backend must sign an empty-body readiness request with its actual Worker MAC")
	require.Equal(t, "not_ready", status)
	require.Equal(t, map[string]bool{
		"document_processing": true,
		"ocr":                 false,
		"office_to_pdf":       true,
		"pdf_to_office":       true,
	}, capabilities)
}

func TestWorkerReadinessProbeDoesNotFollowRedirects(t *testing.T) {
	const secret = "synthetic-readiness-redirect-test-secret"
	t.Setenv("WORKER_SHARED_SECRET", secret)

	targetRequests := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ready","capabilities":{"document_processing":true,"ocr":true,"office_to_pdf":true,"pdf_to_office":true}}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	t.Setenv("PDFNEST_WORKER_URL", redirector.URL)

	_, _, err := probeWorkerReadiness(context.Background())
	require.Error(t, err, "a redirect is not a valid Worker readiness response")
	select {
	case <-targetRequests:
		t.Fatal("signed Worker request was forwarded to a redirect target")
	default:
	}
}
