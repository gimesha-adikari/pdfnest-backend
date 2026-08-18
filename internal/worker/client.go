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

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.New().String()

	method := strings.ToUpper(req.Method)
	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	bodyHash := req.Header.Get("X-Worker-Body-Hash")
	var stringToSign string
	if bodyHash != "" {
		stringToSign = fmt.Sprintf("%s\n%s\n%s\n%s\n%s", method, path, timestamp, nonce, bodyHash)
	} else {
		stringToSign = fmt.Sprintf("%s\n%s\n%s\n%s", method, path, timestamp, nonce)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-Worker-Signature", signature)
	req.Header.Set("X-Worker-Timestamp", timestamp)
	req.Header.Set("X-Worker-Nonce", nonce)

	return nil
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
	base := st.transport
	if base == nil {
		base = http.DefaultTransport
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
) (io.Reader, string, error) {
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
