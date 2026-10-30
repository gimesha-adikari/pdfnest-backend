package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func GetWorkerSharedSecret() string {
	secret := os.Getenv("WORKER_SHARED_SECRET")
	if strings.TrimSpace(secret) == "" {
		return "dev-secret-change-in-production"
	}
	return strings.TrimSpace(secret)
}

func SignRequestWithSecret(req *http.Request, secret string) error {
	if req == nil {
		return fmt.Errorf("nil request")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	// Discard all caller claims, even if staging fails. Only the bytes that
	// become req.Body below may be authenticated.
	for _, name := range []string{"X-Worker-Body-Hash", "X-Worker-Signature", "X-Worker-Timestamp", "X-Worker-Nonce"} {
		req.Header.Del(name)
	}
	body, bodyHash, size, err := stageRequestBody(req.Context(), req.Body, MaxRequestBodyBytes)
	if err != nil {
		return fmt.Errorf("stage worker request body: %w", err)
	}
	if req.URL == nil || strings.TrimSpace(secret) == "" || (req.ContentLength > 0 && req.ContentLength != size) {
		_ = body.Close()
		return fmt.Errorf("invalid worker request target, credentials or body length")
	}
	req.Body = body
	req.ContentLength = size
	req.TransferEncoding = nil
	// Automatic transport replay cannot renew a nonce. Deliberate retries must
	// build a fresh request and pass through signing again.
	req.GetBody = nil
	req.Header.Set("X-Worker-Body-Hash", bodyHash)

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.New().String()

	method := strings.ToUpper(req.Method)
	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	signature := bodySignature(method, path, timestamp, nonce, bodyHash, secret)

	req.Header.Set("X-Worker-Signature", signature)
	req.Header.Set("X-Worker-Timestamp", timestamp)
	req.Header.Set("X-Worker-Nonce", nonce)

	return nil
}

func bodySignature(method, target, timestamp, nonce, digest, secret string) string {
	canonical := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", strings.ToUpper(method), target, timestamp, nonce, digest)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func SignRequest(req *http.Request) error {
	secret := GetWorkerSharedSecret()
	return SignRequestWithSecret(req, secret)
}

type signingTransport struct {
	transport http.RoundTripper
}

func (st *signingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := SignRequest(req); err != nil {
		return nil, err
	}
	defer req.Body.Close()
	base := st.transport
	if base == nil {
		base = http.DefaultTransport
	}
	store := currentDurabilityStore()
	if store != nil {
		return store.durableRoundTrip(req, base)
	}
	if durabilityRequired() && (queuedSubmission(req.URL.Path) || strings.Contains(req.URL.Path, "/jobs/")) {
		return nil, ErrDurabilityUnavailable
	}
	return base.RoundTrip(req)
}

var Client = &http.Client{
	Timeout:   30 * time.Minute,
	Transport: &signingTransport{transport: http.DefaultTransport},
}

func Do(req *http.Request) (*http.Response, error) {
	return Client.Do(req)
}

func GetWorkerURL() string {
	workerURL := os.Getenv("PDFNEST_WORKER_URL")
	if strings.TrimSpace(workerURL) == "" {
		return "http://localhost:8000"
	}
	return strings.TrimRight(workerURL, "/")
}

func CreateMultipartRequest(
	inputPath string,
	build func(*multipart.Writer) error,
) (io.ReadCloser, string, error) {
	file, err := os.Open(inputPath)
	if err != nil {
		return nil, "", err
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		defer file.Close()

		part, err := writer.CreateFormFile(
			"file",
			filepath.Base(inputPath),
		)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, file); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if build != nil {
			if err := build(writer); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}

		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		_ = pw.Close()
	}()

	return pr, contentType, nil
}
