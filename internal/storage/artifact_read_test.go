package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"
)

func requireArtifactKind(t *testing.T, err error, kind ArtifactReadKind) {
	t.Helper()
	var classified *ArtifactReadError
	require.ErrorAs(t, err, &classified)
	require.Equal(t, kind, classified.Kind)
}
func localArtifactEnvironment(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", root)
	t.Setenv("TMPDIR", t.TempDir())
	return root
}
func TestArtifactLocalReadback(t *testing.T) {
	root := localArtifactEnvironment(t)
	ctx := context.Background()
	key := "legacy/artifact-read/fixture.pdf"
	require.NoError(t, SaveObjectBytes(ctx, key, []byte("%PDF-logical-one"), "application/pdf"))
	snapshot, err := ReadArtifact(ctx, key, 1024)
	require.NoError(t, err)
	require.NotEqual(t, filepath.Join(root, filepath.FromSlash(key)), snapshot.Path)
	// Outside the reserved namespace, the shared adapter still replaces objects. The open snapshot is
	// independent; randomness is not an immutable/conditional-write guarantee.
	require.NoError(t, SaveObjectBytes(ctx, key, []byte("%PDF-logical-two"), "application/pdf"))
	data, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	require.Equal(t, "%PDF-logical-one", string(data))
	require.NoError(t, snapshot.Cleanup())
	require.NoError(t, snapshot.Cleanup())
	_, err = os.Stat(snapshot.Path)
	require.True(t, os.IsNotExist(err))
	persistent, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
	require.NoError(t, err)
	require.Equal(t, "%PDF-logical-two", string(persistent))
}
func TestArtifactStrictLocalErrors(t *testing.T) {
	root := localArtifactEnvironment(t)
	for _, key := range []string{"/tmp/file.pdf", "../file.pdf", "a/../file.pdf", "a//file.pdf", "a\\b.pdf", " a.pdf", "a.pdf ", "file:///tmp/file.pdf", "a%2fb.pdf"} {
		_, err := ReadArtifact(context.Background(), key, 1024)
		requireArtifactKind(t, err, ArtifactInvalidKey)
	}
	_, err := ReadArtifact(context.Background(), "missing.pdf", 1024)
	requireArtifactKind(t, err, ArtifactMissing)
	require.NoError(t, SaveObjectBytes(context.Background(), "large.pdf", []byte("12345"), "application/pdf"))
	_, err = ReadArtifact(context.Background(), "large.pdf", 4)
	requireArtifactKind(t, err, ArtifactTooLarge)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.pdf"), []byte("synthetic outside"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	_, err = ReadArtifact(context.Background(), "escape/secret.pdf", 1024)
	require.Error(t, err)
	var classified *ArtifactReadError
	require.ErrorAs(t, err, &classified)
	require.NotEqual(t, ArtifactMissing, classified.Kind)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	_, err = ReadArtifact(context.Background(), "large.pdf", 1024)
	requireArtifactKind(t, err, ArtifactTemporary)
}

// This is a loopback S3 protocol simulation, not R2 or a real S3 service.
// It exercises the actual MinIO GET and production GCM implementation.
func fixtureArtifactRemote(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("fixture-access", "fixture-secret", ""), Secure: false, Region: "us-east-1", MaxRetries: 1})
	require.NoError(t, err)
	return &Store{client: client, bucket: "synthetic"}
}
func TestArtifactRemoteLogicalDecryption(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	var buffer bytes.Buffer
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 12)
	pdf.Cell(100, 10, "S211_REMOTE_LOGICAL")
	require.NoError(t, pdf.Output(&buffer))
	logical := buffer.Bytes()
	encrypted, err := encryptData(logical)
	require.NoError(t, err)
	require.NotEqual(t, logical, encrypted)
	var ciphertext atomic.Value
	ciphertext.Store(encrypted)
	store := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/synthetic/private/fixture.pdf", r.URL.Path)
		payload := ciphertext.Load().([]byte)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("ETag", `"opaque-not-a-logical-digest"`)
		_, _ = w.Write(payload)
	})
	first, err := store.ReadArtifact(context.Background(), "private/fixture.pdf", 1024*1024)
	require.NoError(t, err)
	data, err := os.ReadFile(first.Path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(logical), sha256.Sum256(data))
	file, openErr := os.Open(first.Path)
	require.NoError(t, openErr)
	conf := model.NewDefaultConfiguration()
	conf.Cmd = model.VALIDATE
	parsed, parseErr := api.ReadAndValidate(file, conf)
	require.NoError(t, parseErr)
	require.Equal(t, 1, parsed.PageCount)
	require.Nil(t, parsed.Encrypt)
	require.NoError(t, file.Close())
	require.NoError(t, first.Cleanup())
	// Same logical PDF, different nonce/ciphertext: logical evidence is unchanged.
	next, err := encryptData(logical)
	ciphertext.Store(next)
	require.NoError(t, err)
	require.NotEqual(t, encrypted, next)
	second, err := store.ReadArtifact(context.Background(), "private/fixture.pdf", 1024*1024)
	require.NoError(t, err)
	data, err = os.ReadFile(second.Path)
	require.NoError(t, err)
	require.Equal(t, logical, data)
	require.NoError(t, second.Cleanup())
	corrupt := append([]byte(nil), next...)
	corrupt[len(corrupt)-1] ^= 1
	ciphertext.Store(corrupt)
	_, err = store.ReadArtifact(context.Background(), "private/fixture.pdf", 1024*1024)
	requireArtifactKind(t, err, ArtifactIntegrity)
	files, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "ocr-artifact-read-*"))
	require.NoError(t, err)
	require.Empty(t, files)
}
func TestArtifactRemoteFailureClassification(t *testing.T) {
	for _, scenario := range []struct {
		name, code string
		status     int
		kind       ArtifactReadKind
	}{
		{"missing", "NoSuchKey", 404, ArtifactMissing}, {"bucket_missing", "NoSuchBucket", 404, ArtifactUnavailable}, {"denied", "AccessDenied", 403, ArtifactDenied}, {"timeout", "RequestTimeout", 408, ArtifactUnavailable}, {"outage", "ServiceUnavailable", 503, ArtifactUnavailable}, {"retention", "ObjectLockedByBucketPolicy", 409, ArtifactUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			store := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(scenario.status)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic error</Message></Error>", scenario.code)
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := store.ReadArtifact(ctx, "private/fixture.pdf", 1024*1024)
			requireArtifactKind(t, err, scenario.kind)
			files, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "ocr-artifact-read-*"))
			require.Empty(t, files)
		})
	}
	t.Run("partial_read", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		t.Setenv("FILE_ENCRYPTION_KEY", "")
		store := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "%PDF-truncated")
		})
		_, err := store.ReadArtifact(context.Background(), "private/fixture.pdf", 1024*1024)
		requireArtifactKind(t, err, ArtifactUnavailable)
	})
	t.Run("transport", func(t *testing.T) {
		store := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := store.ReadArtifact(ctx, "private/fixture.pdf", 1024*1024)
		requireArtifactKind(t, err, ArtifactUnavailable)
	})
}
func TestArtifactReadCancellationAndLimits(t *testing.T) {
	localArtifactEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := snapshotArtifact(ctx, io.NopCloser(strings.NewReader("content")), 1024, false)
	requireArtifactKind(t, err, ArtifactUnavailable)
	_, err = ReadArtifact(context.Background(), "safe.pdf", 0)
	requireArtifactKind(t, err, ArtifactTooLarge)
	_, err = ReadArtifact(context.Background(), "safe.pdf", 251*1024*1024)
	requireArtifactKind(t, err, ArtifactTooLarge)
	requireArtifactKind(t, classifyArtifactRead(os.ErrPermission), ArtifactDenied)
	requireArtifactKind(t, classifyArtifactRead(errors.New("ambiguous read acknowledgement")), ArtifactUnavailable)
}
