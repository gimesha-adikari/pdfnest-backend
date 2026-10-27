package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/studiosource"
)

func studioStorageLocal(t *testing.T) string {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)
	t.Setenv("TMPDIR", t.TempDir())
	return root
}
func studioStorageFile(t *testing.T, root, key string, size int64) {
	t.Helper()
	path := filepath.Join(root, key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}
func TestStudioPDFStagingBoundariesAndCleanup(t *testing.T) {
	root := studioStorageLocal(t)
	for _, size := range []int64{studiosource.MaxPDFBytes - 1, studiosource.MaxPDFBytes, studiosource.MaxPDFBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			studioStorageFile(t, root, "owned/source.pdf", size)
			path, cleanup, err := StageStudioPDF(context.Background(), "owned/source.pdf")
			if size > studiosource.MaxPDFBytes {
				require.ErrorIs(t, err, ErrStudioPDFTooLarge)
				require.Nil(t, cleanup)
				require.Empty(t, path)
			} else {
				require.NoError(t, err)
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, size, info.Size())
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
				cleanup()
				cleanup()
				_, err = os.Stat(path)
				require.True(t, os.IsNotExist(err))
			}
			require.Empty(t, studioSpools(t))
			require.Zero(t, len(studioPDFSlots))
		})
	}
}
func studioSpools(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), "platen-studio-source-*"))
	require.NoError(t, err)
	return paths
}

type countedStudioReader struct{ bytes int }

func (r *countedStudioReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 'x'
	}
	r.bytes += len(b)
	return len(b), nil
}
func TestStudioPDFActualReadCeilingAndMisleadingLength(t *testing.T) {
	studioStorageLocal(t)
	r := &countedStudioReader{}
	_, err := stageStudioPDF(context.Background(), r, 8, 1)
	require.ErrorIs(t, err, ErrStudioPDFTooLarge)
	require.Equal(t, 9, r.bytes)
	require.Empty(t, studioSpools(t))
	_, err = stageStudioPDF(context.Background(), strings.NewReader("1234"), 8, 1)
	require.ErrorIs(t, err, ErrStudioPDFChanged)
	require.Empty(t, studioSpools(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = stageStudioPDF(ctx, strings.NewReader("1234"), 8, 4)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, studioSpools(t))
}
func TestStudioPDFSlotsAndLocalReadFailures(t *testing.T) {
	root := studioStorageLocal(t)
	studioStorageFile(t, root, "small.pdf", 4)
	var cleanups []func()
	for range 4 {
		_, cleanup, err := StageStudioPDF(context.Background(), "small.pdf")
		require.NoError(t, err)
		cleanups = append(cleanups, cleanup)
	}
	_, _, err := StageStudioPDF(context.Background(), "small.pdf")
	require.ErrorIs(t, err, ErrStudioPDFUnavailable)
	for _, cleanup := range cleanups {
		cleanup()
	}
	require.Zero(t, len(studioPDFSlots))
	require.Empty(t, studioSpools(t))
	_, _, err = StageStudioPDF(context.Background(), "missing.pdf")
	require.ErrorIs(t, err, ErrObjectNotFound)
	require.NoError(t, os.Chmod(filepath.Join(root, "small.pdf"), 0000))
	_, _, err = StageStudioPDF(context.Background(), "small.pdf")
	require.ErrorIs(t, err, ErrStudioPDFUnavailable)
	require.NoError(t, os.Chmod(filepath.Join(root, "small.pdf"), 0600))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "outside.pdf")))
	_, _, err = StageStudioPDF(context.Background(), "outside.pdf")
	require.ErrorIs(t, err, ErrStudioPDFUnavailable)
	require.Zero(t, len(studioPDFSlots))
	require.Empty(t, studioSpools(t))
}

func studioRemoteStore(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("synthetic", "synthetic", ""), Secure: false, Region: "us-east-1"})
	require.NoError(t, err)
	oldStore, oldErr := defaultStore, defaultErr
	t.Cleanup(func() {
		defaultStore, defaultErr = oldStore, oldErr
		once = sync.Once{}
		if oldStore != nil || oldErr != nil {
			once.Do(func() {})
		}
	})
	defaultStore = &Store{client: client, bucket: "fixture"}
	defaultErr = nil
	once = sync.Once{}
	once.Do(func() {})
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "remote")
	t.Setenv("TMPDIR", t.TempDir())
}
func TestStudioPDFRemoteConditionalReadAndFailures(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "unavailable", "changed-before-get", "changed-response", "length-lie", "oversize-head"} {
		t.Run(scenario, func(t *testing.T) {
			var gotCondition atomic.Bool
			var gets atomic.Int32
			studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario == "missing" {
					w.WriteHeader(404)
					return
				}
				if scenario == "unavailable" {
					w.WriteHeader(403)
					return
				}
				w.Header().Set("ETag", `"initial"`)
				w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
				if r.Method == "HEAD" {
					size := 4
					if scenario == "oversize-head" {
						size = int(studiosource.MaxPDFBytes + 1)
					}
					w.Header().Set("Content-Length", fmt.Sprint(size))
					return
				}
				gets.Add(1)
				gotCondition.Store(r.Header.Get("If-Match") == `"initial"`)
				if scenario == "changed-before-get" {
					w.WriteHeader(412)
					return
				}
				if scenario == "changed-response" {
					w.Header().Set("ETag", `"replacement"`)
				}
				body := "%PDF"
				if scenario == "length-lie" {
					body = "longer-body"
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = io.WriteString(w, body)
			})
			path, cleanup, err := StageStudioPDF(context.Background(), "owned/source.pdf")
			switch scenario {
			case "valid":
				require.NoError(t, err)
				data, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, "%PDF", string(data))
				cleanup()
				require.True(t, gotCondition.Load())
			case "missing":
				require.ErrorIs(t, err, ErrObjectNotFound)
			case "unavailable":
				require.ErrorIs(t, err, ErrStudioPDFUnavailable)
			case "oversize-head":
				require.ErrorIs(t, err, ErrStudioPDFTooLarge)
				require.Zero(t, gets.Load())
			default:
				require.ErrorIs(t, err, ErrStudioPDFChanged)
			}
			require.Zero(t, len(studioPDFSlots))
			require.Empty(t, studioSpools(t))
		})
	}
}
func TestStudioPDFRemoteCancellationAndDeadline(t *testing.T) {
	for _, scenario := range []string{"cancel", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			started, finished := make(chan struct{}), make(chan struct{})
			studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"initial"`)
				w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
				w.Header().Set("Content-Length", "8")
				if r.Method == "HEAD" {
					return
				}
				w.WriteHeader(200)
				_, _ = io.WriteString(w, "%PDF")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(finished)
			})
			ctx, cancel := context.WithCancel(context.Background())
			if scenario == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := StageStudioPDF(ctx, "source.pdf"); done <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not start")
			}
			if scenario == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, ErrStudioPDFUnavailable)
			case <-time.After(3 * time.Second):
				t.Fatal("acquisition did not stop")
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request leaked")
			}
			require.Zero(t, len(studioPDFSlots))
			require.Empty(t, studioSpools(t))
		})
	}
}
func TestStudioPDFPublishBoundedAndContextAware(t *testing.T) {
	root := studioStorageLocal(t)
	path := filepath.Join(t.TempDir(), "source.pdf")
	require.NoError(t, os.WriteFile(path, []byte("%PDF-fixture"), 0600))
	require.NoError(t, SaveStudioPDF(context.Background(), "studio/sources/synthetic.pdf", path))
	require.FileExists(t, filepath.Join(root, "studio/sources/synthetic.pdf"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, SaveStudioPDF(ctx, "studio/sources/cancelled.pdf", path))
	require.NoFileExists(t, filepath.Join(root, "studio/sources/cancelled.pdf"))
	reader := &studioPublishReader{source: strings.NewReader("extra"), count: studiosource.MaxPDFBytes}
	n, err := reader.Read(make([]byte, 4))
	require.Zero(t, n)
	require.True(t, errors.Is(err, ErrStudioPDFTooLarge))
}

func TestStudioPDFRemoteActualOversizeDespiteSmallHEAD(t *testing.T) {
	studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"initial"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "4")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(studiosource.MaxPDFBytes+4096))
		_, _ = io.CopyN(w, &countedStudioReader{}, studiosource.MaxPDFBytes+4096)
	})
	_, cleanup, err := StageStudioPDF(context.Background(), "owned/source.pdf")
	require.ErrorIs(t, err, ErrStudioPDFTooLarge)
	require.Nil(t, cleanup)
	require.Empty(t, studioSpools(t))
	require.Zero(t, len(studioPDFSlots))
}

func TestStudioPDFRepeatedLocalDescriptorCleanup(t *testing.T) {
	root := studioStorageLocal(t)
	studioStorageFile(t, root, "small.pdf", 4)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("descriptor accounting requires procfs")
	}
	for range 32 {
		_, cleanup, err := StageStudioPDF(context.Background(), "small.pdf")
		require.NoError(t, err)
		cleanup()
		_, _, err = StageStudioPDF(context.Background(), "missing.pdf")
		require.ErrorIs(t, err, ErrObjectNotFound)
	}
	after, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	require.LessOrEqual(t, len(after), len(before), "file descriptors must not accumulate")
	require.Empty(t, studioSpools(t))
	require.Zero(t, len(studioPDFSlots))
}

func TestStudioPDFRemotePublicationPreservesEncryption(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprint(encrypted), func(t *testing.T) {
			t.Setenv("FILE_ENCRYPTION_KEY", "")
			if encrypted {
				t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
			}
			received := make(chan []byte, 1)
			studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.Path != "/fixture/studio/sources/synthetic.pdf" {
					w.WriteHeader(400)
					return
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
				if err != nil {
					w.WriteHeader(400)
					return
				}
				// The fixture emulates S3 decoding of AWS signed transfer chunks.
				if r.Header.Get("Content-Encoding") == "aws-chunked" || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
					body, err = io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(body)))
					if err != nil {
						w.WriteHeader(400)
						return
					}
				}
				received <- body
				w.Header().Set("ETag", `"synthetic"`)
			})
			original := []byte("%PDF-1.7 synthetic publication")
			path := filepath.Join(t.TempDir(), "source.pdf")
			require.NoError(t, os.WriteFile(path, original, 0600))
			require.NoError(t, SaveStudioPDF(context.Background(), "studio/sources/synthetic.pdf", path))
			body := <-received
			if encrypted {
				require.NotEqual(t, original, body)
			}
			plain, err := decryptData(body)
			require.NoError(t, err)
			require.Equal(t, original, plain)
		})
	}
}
