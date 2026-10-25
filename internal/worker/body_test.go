package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func checkBodySignature(t *testing.T, req *http.Request, secret string) []byte {
	t.Helper()
	body, err := io.ReadAll(req.Body) // Test fixtures only; never production signing.
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	encoded := hex.EncodeToString(digest[:])
	require.Equal(t, encoded, req.Header.Get("X-Worker-Body-Hash"))
	canonical := strings.ToUpper(req.Method) + "\n" + req.URL.Path
	if req.URL.RawQuery != "" {
		canonical += "?" + req.URL.RawQuery
	}
	canonical += "\n" + req.Header.Get("X-Worker-Timestamp") + "\n" + req.Header.Get("X-Worker-Nonce") + "\n" + encoded
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	require.Equal(t, hex.EncodeToString(mac.Sum(nil)), req.Header.Get("X-Worker-Signature"))
	require.Nil(t, req.GetBody, "transport replay cannot reuse authentication nonces")
	return body
}

func TestBodySignatureUsesActualBytesAndEmptyBody(t *testing.T) {
	for _, data := range []string{"", `{"operation":"ocr","name":"café"}`, "multipart\x00\r\nbytes"} {
		req, err := http.NewRequest("POST", "http://worker/internal/test?a=%2F&b=one+two", strings.NewReader(data))
		require.NoError(t, err)
		req.Header.Set("X-Worker-Body-Hash", strings.Repeat("a", 64))
		require.NoError(t, SignRequestWithSecret(req, "synthetic-secret"))
		require.Equal(t, []byte(data), checkBodySignature(t, req, "synthetic-secret"))
		require.NoError(t, req.Body.Close())
	}
}

func TestFixedGoPythonSignatureVector(t *testing.T) {
	// The same public UTF-8/raw-byte vector is verified by the strict Python middleware.
	body := []byte("{\"operation\":\"ocr\",\"name\":\"café\"}\n")
	digest := sha256.Sum256(body)
	encoded := hex.EncodeToString(digest[:])
	require.Equal(t, "c42873a7e4f262babfcc423f3d95d9b8f02c291b62909b16bf8ed583dffe7cbd", encoded)
	require.Equal(t, "6ab65000844eb3a4173ab80ebe8a7dfb79e150d2be4f2cbcbb218f7c265d3128",
		bodySignature("POST", "/internal/ocr/v2/jobs?b=2&a=%2F", "1800000000", "f15-cross-language", encoded, "f15-public-vector-secret"))
}

func TestBodySpoolPrivateBoundedAndRemoved(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	data := bytes.Repeat([]byte("synthetic"), 20000)
	body, digest, size, err := stageRequestBody(context.Background(), io.NopCloser(bytes.NewReader(data)), int64(len(data)))
	require.NoError(t, err)
	file := body.(*stagedBody)
	stat, err := file.Stat()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
	require.Equal(t, int64(len(data)), size)
	expected := sha256.Sum256(data)
	require.Equal(t, hex.EncodeToString(expected[:]), digest)
	actual, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, data, actual)
	require.NoError(t, body.Close())
	require.NoError(t, body.Close(), "cleanup is idempotent")
	_, err = os.Stat(file.Name())
	require.True(t, os.IsNotExist(err))
	require.Empty(t, bodySpoolSlots)
}

func TestLimitAndCancellationUnblockPipeProducer(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "limit", true: "cancel"}[cancelRead], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader, writer := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer writer.Close()
				if cancelRead {
					cancel()
				} else {
					_, _ = writer.Write(bytes.Repeat([]byte("x"), 200<<10))
				}
				_, _ = writer.Write([]byte("producer must not remain blocked"))
			}()
			_, _, _, err := stageRequestBody(ctx, reader, 100<<10)
			if cancelRead {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, ErrRequestBodyTooLarge)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blocked multipart producer")
			}
			matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "platen-worker-body-*"))
			require.Empty(t, matches)
			require.Empty(t, bodySpoolSlots)
		})
	}
}

type failedBody struct{ closed bool }

func (*failedBody) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
func (b *failedBody) Close() error           { b.closed = true; return nil }

func TestSignerReadFailureNeverDispatchesOrRetainsClaims(t *testing.T) {
	body := &failedBody{}
	req, _ := http.NewRequest("POST", "http://worker/test", body)
	req.Header.Set("X-Worker-Signature", "old")
	req.Header.Set("X-Worker-Body-Hash", "old")
	dispatched := false
	_, err := (&signingTransport{transport: transportFunc(func(*http.Request) (*http.Response, error) {
		dispatched = true
		return nil, errors.New("must not dispatch")
	})}).RoundTrip(req)
	require.Error(t, err)
	require.False(t, dispatched)
	require.True(t, body.closed)
	require.Empty(t, req.Header.Get("X-Worker-Signature"))
	require.Empty(t, req.Header.Get("X-Worker-Body-Hash"))
}

func TestConcurrentBodyStateAndFreshRetryIdentity(t *testing.T) {
	var wg sync.WaitGroup
	nonces := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", "http://worker/test", strings.NewReader("small independent request"))
			if err := SignRequestWithSecret(req, "synthetic"); err != nil {
				t.Error(err)
				return
			}
			defer req.Body.Close()
			nonces <- req.Header.Get("X-Worker-Nonce")
		}()
	}
	wg.Wait()
	close(nonces)
	seen := map[string]bool{}
	for nonce := range nonces {
		require.False(t, seen[nonce])
		seen[nonce] = true
	}
	require.Len(t, seen, 8)
}

func TestSpoolCapacityAndTransportFailureCleanup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var bodies []io.ReadCloser
	defer func() {
		for _, b := range bodies {
			_ = b.Close()
		}
	}()
	for i := 0; i < cap(bodySpoolSlots); i++ {
		body, _, _, err := stageRequestBody(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", bodyMemoryBytes+1))), MaxRequestBodyBytes)
		require.NoError(t, err)
		bodies = append(bodies, body)
	}
	_, _, _, err := stageRequestBody(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", bodyMemoryBytes+1))), MaxRequestBodyBytes)
	require.ErrorIs(t, err, ErrBodySpoolCapacity)
	// Empty control/status requests still work while upload spools are occupied.
	empty, _, _, err := stageRequestBody(context.Background(), nil, MaxRequestBodyBytes)
	require.NoError(t, err)
	_ = empty.Close()
	for _, b := range bodies {
		_ = b.Close()
	}
	bodies = nil
	req, _ := http.NewRequest("POST", "http://worker/test", strings.NewReader(strings.Repeat("x", bodyMemoryBytes+1)))
	_, err = (&signingTransport{transport: failingTransport{}}).RoundTrip(req)
	require.Error(t, err)
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "platen-worker-body-*"))
	require.Empty(t, matches)
	require.Empty(t, bodySpoolSlots)
}

type failingTransport struct{}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic transport failure")
}
