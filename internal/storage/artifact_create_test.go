//go:build linux

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/signer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func createFixture(t *testing.T) (string, string, string, int64) {
	t.Helper()
	key := "ocr-finalization/outputs/" + strings.Repeat("a", 64) + "/" + uuid.NewString() + ".pdf"
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(false)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 12)
	pdf.Cell(100, 10, "S212_REMOTE_LOGICAL")
	var buffer bytes.Buffer
	require.NoError(t, pdf.Output(&buffer))
	data := buffer.Bytes()
	source := filepath.Join(t.TempDir(), "source.pdf")
	require.NoError(t, os.WriteFile(source, data, 0600))
	hash := sha256.Sum256(data)
	return key, source, hex.EncodeToString(hash[:]), int64(len(data))
}
func requireCreateKind(t *testing.T, err error, kind ArtifactCreateKind) {
	t.Helper()
	var typed *ArtifactCreateError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, kind, typed.Kind)
}
func TestArtifactExclusiveLocal(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	ctx := context.Background()
	original, err := os.ReadFile(source)
	require.NoError(t, err)
	require.NoError(t, CreateArtifactIfAbsent(ctx, key, source, digest, size))
	for _, different := range []bool{false, true} {
		if different {
			require.NoError(t, os.WriteFile(source, []byte("different bytes"), 0600))
			data, _ := os.ReadFile(source)
			hash := sha256.Sum256(data)
			digest = hex.EncodeToString(hash[:])
			size = int64(len(data))
		}
		requireCreateKind(t, CreateArtifactIfAbsent(ctx, key, source, digest, size), ArtifactCreateExists)
	}
	persistent := filepath.Join(root, key)
	data, err := os.ReadFile(persistent)
	require.NoError(t, err)
	require.Equal(t, original, data)
	info, err := os.Stat(persistent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	stages, err := filepath.Glob(filepath.Join(filepath.Dir(persistent), ".ocr-stage-*"))
	require.NoError(t, err)
	require.Empty(t, stages)
}
func TestArtifactExclusiveLocalConcurrent(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	start := make(chan struct{})
	var successes atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			err := createLocalArtifact(context.Background(), root, key, source, digest, size, localCreateOps{})
			if err == nil {
				successes.Add(1)
			} else {
				var e *ArtifactCreateError
				if !errors.As(err, &e) || (e.Kind != ArtifactCreateExists && e.Kind != ArtifactCreateUncertain) {
					t.Errorf("unexpected: %v", err)
				}
			}
		}()
	}
	close(start)
	group.Wait()
	require.EqualValues(t, 1, successes.Load())
}
func TestArtifactExclusiveLocalConfinement(t *testing.T) {
	localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	for _, invalid := range []string{"/" + key, "../" + key, strings.Replace(key, "outputs/", "outputs/../", 1), strings.Replace(key, "outputs/", "outputs//", 1), strings.ToUpper(key), "source/" + key, key + " ", strings.TrimSuffix(key, ".pdf") + ".bin", strings.Replace(key, "/", "\\", 1)} {
		requireCreateKind(t, CreateArtifactIfAbsent(context.Background(), invalid, source, digest, size), ArtifactCreateInvalid)
	}
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			local := t.TempDir()
			target := filepath.Join(local, "other")
			if outside {
				target = t.TempDir()
			} else {
				require.NoError(t, os.Mkdir(target, 0700))
			}
			require.NoError(t, os.Symlink(target, filepath.Join(local, "ocr-finalization")))
			require.Error(t, createLocalArtifact(context.Background(), local, key, source, digest, size, localCreateOps{}))
			entries, err := os.ReadDir(target)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
func TestArtifactExclusiveLocalFailureBoundaries(t *testing.T) {
	for _, scenario := range []string{"digest", "size", "file_sync", "copy_failure", "directory_sync", "publish_unknown", "unsupported", "cancel_before", "cancel_staged"} {
		t.Run(scenario, func(t *testing.T) {
			root := localArtifactEnvironment(t)
			key, source, digest, size := createFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := localCreateOps{}
			kind := ArtifactCreateNotApplied
			committed := false
			switch scenario {
			case "digest":
				digest = strings.Repeat("0", 64)
				kind = ArtifactCreateInvalid
			case "size":
				size++
				kind = ArtifactCreateInvalid
			case "copy_failure":
				ops.copy = func(w io.Writer, r io.Reader) (int64, error) {
					n, _ := io.CopyN(w, r, 8)
					return n, io.ErrUnexpectedEOF
				}
			case "file_sync":
				ops.fileSync = func(*os.File) error { return errors.New("synthetic sync") }
			case "directory_sync":
				ops.directorySync = func(int) error { return errors.New("synthetic directory sync") }
				kind = ArtifactCreateUncertain
				committed = true
			case "publish_unknown":
				ops.publish = func(int, string, string) error { return errors.New("unknown") }
				kind = ArtifactCreateUncertain
			case "unsupported":
				ops.publish = func(int, string, string) error { return unix.ENOTSUP }
				kind = ArtifactCreateUnsupported
			case "cancel_before":
				cancel()
			case "cancel_staged":
				ops.staged = cancel
			}
			if scenario != "cancel_before" {
				prior := ops.staged
				ops.staged = func() {
					_, err := os.Stat(filepath.Join(root, key))
					require.True(t, os.IsNotExist(err), "final must not be visible during staging")
					if prior != nil {
						prior()
					}
				}
			}
			requireCreateKind(t, createLocalArtifact(ctx, root, key, source, digest, size, ops), kind)
			_, err := os.Stat(filepath.Join(root, key))
			if committed {
				require.NoError(t, err)
			} else {
				require.True(t, os.IsNotExist(err))
			}
			stages, _ := filepath.Glob(filepath.Join(root, filepath.Dir(key), ".ocr-stage-*"))
			require.Empty(t, stages)
		})
	}
}

// Helper runs in a separate OS process, with no shared application mutex.
func TestArtifactExclusiveProcessHelper(t *testing.T) {
	if os.Getenv("S212_CHILD") != "1" {
		t.Skip("subprocess helper only")
	}
	var signal [1]byte
	_, err := os.Stdin.Read(signal[:])
	require.NoError(t, err)
	ctx := context.Background()
	root, key, source, digest := os.Getenv("S212_ROOT"), os.Getenv("S212_KEY"), os.Getenv("S212_SOURCE"), os.Getenv("S212_DIGEST")
	info, err := os.Stat(source)
	require.NoError(t, err)
	ops := localCreateOps{}
	if os.Getenv("S212_CRASH") == "1" {
		ops.staged = func() { fmt.Fprintln(os.Stdout, "STAGED"); var block [1]byte; _, _ = os.Stdin.Read(block[:]) }
	}
	err = createLocalArtifact(ctx, root, key, source, digest, info.Size(), ops)
	if err == nil {
		fmt.Fprintln(os.Stdout, "WIN")
	} else {
		var e *ArtifactCreateError
		require.ErrorAs(t, err, &e)
		fmt.Fprintln(os.Stdout, e.Kind)
	}
}
func artifactChild(t *testing.T, root, key, source, digest string, crash bool) (*exec.Cmd, io.WriteCloser, io.ReadCloser) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArtifactExclusiveProcessHelper$")
	cmd.Env = append(os.Environ(), "S212_CHILD=1", "S212_ROOT="+root, "S212_KEY="+key, "S212_SOURCE="+source, "S212_DIGEST="+digest)
	if crash {
		cmd.Env = append(cmd.Env, "S212_CRASH=1")
	}
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	return cmd, in, out
}
func TestArtifactExclusiveCrossProcess(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, _ := createFixture(t)
	a, ai, ao := artifactChild(t, root, key, source, digest, false)
	other := filepath.Join(t.TempDir(), "competing.pdf")
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	data = bytes.ReplaceAll(data, []byte("S212_REMOTE_LOGICAL"), []byte("S212_COMPETING_FILE"))
	require.NoError(t, os.WriteFile(other, data, 0600))
	h := sha256.Sum256(data)
	b, bi, bo := artifactChild(t, root, key, other, hex.EncodeToString(h[:]), false)
	_, err = ai.Write([]byte{1})
	require.NoError(t, err)
	_, err = bi.Write([]byte{1})
	require.NoError(t, err)
	ad, err := io.ReadAll(ao)
	require.NoError(t, err)
	bd, err := io.ReadAll(bo)
	require.NoError(t, err)
	require.NoError(t, a.Wait())
	require.NoError(t, b.Wait())
	require.Equal(t, 1, strings.Count(string(ad)+string(bd), "WIN"))
}
func TestArtifactExclusiveStagingCrash(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	cmd, in, out := artifactChild(t, root, key, source, digest, true)
	defer in.Close()
	_, err := in.Write([]byte{1})
	require.NoError(t, err)
	signal := make([]byte, len("STAGED\n"))
	_, err = io.ReadFull(out, signal)
	require.NoError(t, err)
	require.Equal(t, "STAGED\n", string(signal))
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	_, err = os.Stat(filepath.Join(root, key))
	require.True(t, os.IsNotExist(err))
	for i := 0; i < 3; i++ {
		requireCreateKind(t, createLocalArtifact(context.Background(), root, key, source, digest, size, localCreateOps{}), ArtifactCreateUncertain)
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.Dir(key)))
	require.NoError(t, err)
	require.Len(t, entries, 1, "one retained staging slot; no retry accumulation")
}

// Actual MinIO HTTP/signing, simulated provider atomic commit. No real R2.
func exclusiveRemote(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("fixture-access", "fixture-secret", ""), Secure: false, Region: "us-east-1", MaxRetries: 4})
	require.NoError(t, err)
	return &Store{client: client, bucket: "synthetic", artifactConditionalTest: true}
}
func checkConditionalRequest(t *testing.T, r *http.Request, key string) {
	t.Helper()
	assert.Equal(t, "PUT", r.Method)
	assert.Equal(t, "/synthetic/"+key, r.URL.Path)
	assert.Empty(t, r.URL.RawQuery)
	assert.Equal(t, "*", r.Header.Get("If-None-Match"))
	auth := r.Header.Get("Authorization")
	assert.Contains(t, auth, "if-none-match")
	// Fixture-only signature verification uses the transmitted timestamp; calling
	// SignV4 again would use time.Now and become flaky across a second boundary.
	// This is not a general-purpose S3 request signer or production transport.
	signedHeaders := "content-type;host;if-none-match;x-amz-content-sha256;x-amz-date"
	date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "application/pdf", r.Header.Get("Content-Type"))
	assert.Equal(t, "UNSIGNED-PAYLOAD", r.Header.Get("X-Amz-Content-Sha256"))
	canonicalHeaders := strings.Join([]string{
		"content-type:" + r.Header.Get("Content-Type"), "host:" + r.Host,
		"if-none-match:" + r.Header.Get("If-None-Match"),
		"x-amz-content-sha256:" + r.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date:" + r.Header.Get("X-Amz-Date"), ""}, "\n")
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), "", canonicalHeaders, signedHeaders, "UNSIGNED-PAYLOAD"}, "\n")
	hash := sha256.Sum256([]byte(canonical))
	scope := date.Format("20060102") + "/us-east-1/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	signature := signer.PostPresignSignatureV4(toSign, date, "fixture-secret", "us-east-1")
	expected := "AWS4-HMAC-SHA256 Credential=fixture-access/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	assert.Equal(t, expected, auth)
}
func s3Failure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic</Message></Error>", code)
}
func TestArtifactExclusiveRemoteConcurrent(t *testing.T) {
	t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	key, source, digest, size := createFixture(t)
	var mu sync.Mutex
	var object []byte
	var received [][]byte
	var puts int
	store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Length", fmt.Sprint(len(object)))
			w.Header().Set("ETag", "\"ciphertext-not-logical\"")
			_, _ = w.Write(object)
			return
		}
		checkConditionalRequest(t, r, key)
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		puts++
		received = append(received, data)
		if object != nil {
			s3Failure(w, 412, "PreconditionFailed")
			return
		}
		object = append([]byte(nil), data...)
		w.Header().Set("ETag", "\"ciphertext-not-logical\"")
	})
	start := make(chan struct{})
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			<-start
			results <- store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size)
		}()
	}
	close(start)
	winners := 0
	for i := 0; i < 6; i++ {
		err := <-results
		if err == nil {
			winners++
		} else {
			requireCreateKind(t, err, ArtifactCreateExists)
		}
	}
	require.Equal(t, 1, winners)
	mu.Lock()
	require.Equal(t, 6, puts)
	require.NotEqual(t, received[0], received[1], "GCM nonces change ciphertext, not logical intent")
	saved := append([]byte(nil), object...)
	mu.Unlock()
	different := []byte("different logical PDF")
	require.NoError(t, os.WriteFile(source, different, 0600))
	h := sha256.Sum256(different)
	requireCreateKind(t, store.CreateArtifactIfAbsent(context.Background(), key, source, hex.EncodeToString(h[:]), int64(len(different))), ArtifactCreateExists)
	mu.Lock()
	require.Equal(t, saved, object)
	mu.Unlock()
	snapshot, err := store.ReadArtifact(context.Background(), key, 1024*1024)
	require.NoError(t, err)
	defer snapshot.Cleanup()
	logical, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	hash := sha256.Sum256(logical)
	require.Equal(t, digest, hex.EncodeToString(hash[:]))
	for _, check := range []struct {
		name     string
		args     []string
		contains string
	}{
		{"qpdf", []string{"--check", snapshot.Path}, "No syntax or stream encoding errors found"},
		{"pdfinfo", []string{snapshot.Path}, "Pages:           1"},
		{"pdftotext", []string{snapshot.Path, "-"}, "S212_REMOTE_LOGICAL"},
	} {
		output, err := exec.Command(check.name, check.args...).CombinedOutput()
		require.NoError(t, err, string(output))
		require.Contains(t, string(output), check.contains)
	}
}
func TestArtifactExclusiveRemoteFailures(t *testing.T) {
	for _, scenario := range []string{"conflict", "denied", "unsupported", "unavailable", "lost_ack", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			key, source, digest, size := createFixture(t)
			var puts atomic.Int32
			var applied atomic.Bool
			store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
				checkConditionalRequest(t, r, key)
				puts.Add(1)
				switch scenario {
				case "conflict":
					s3Failure(w, 409, "ConditionalRequestConflict")
				case "denied":
					s3Failure(w, 403, "AccessDenied")
				case "unsupported":
					s3Failure(w, 501, "NotImplemented")
				case "unavailable":
					s3Failure(w, 503, "ServiceUnavailable")
				case "lost_ack", "interrupted":
					if scenario == "lost_ack" {
						_, err := io.Copy(io.Discard, r.Body)
						require.NoError(t, err)
						applied.Store(true)
					}
					connection, _, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					_ = connection.Close()
				}
			})
			kind := ArtifactCreateUncertain
			switch scenario {
			case "conflict":
				kind = ArtifactCreateConflict
			case "denied":
				kind = ArtifactCreateDenied
			case "unsupported":
				kind = ArtifactCreateUnsupported
			}
			requireCreateKind(t, store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size), kind)
			require.EqualValues(t, 1, puts.Load(), "SDK may not replay an ambiguous conditional PUT")
			require.Equal(t, scenario == "lost_ack", applied.Load())
		})
	}
}
func TestArtifactExclusiveRemoteInflightAbsence(t *testing.T) {
	key, source, digest, size := createFixture(t)
	started, commit := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var object []byte
	store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mu.Lock()
			defer mu.Unlock()
			if object == nil {
				s3Failure(w, 404, "NoSuchKey")
				return
			}
			w.Header().Set("ETag", "\"synthetic\"")
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Length", fmt.Sprint(len(object)))
			_, _ = w.Write(object)
			return
		}
		checkConditionalRequest(t, r, key)
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		close(started)
		<-commit
		mu.Lock()
		object = data
		mu.Unlock()
		w.Header().Set("ETag", "\"synthetic\"")
	})
	done := make(chan error, 1)
	go func() { done <- store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT did not reach controlled barrier")
	}
	_, err := store.ReadArtifact(context.Background(), key, 1024*1024)
	requireArtifactKind(t, err, ArtifactMissing)
	close(commit)
	require.NoError(t, <-done)
	snapshot, err := store.ReadArtifact(context.Background(), key, 1024*1024)
	require.NoError(t, err)
	defer snapshot.Cleanup()
	data, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	original, err := os.ReadFile(source)
	require.NoError(t, err)
	require.True(t, bytes.Equal(original, data))
}

func TestArtifactExclusiveUnsupportedProvider(t *testing.T) {
	key, source, digest, size := createFixture(t)
	for _, endpoint := range []struct {
		host   string
		secure bool
	}{{"unqualified.example", true}, {"127.0.0.1:1", false}, {"0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com", false}} {
		client, err := minio.New(endpoint.host, &minio.Options{Creds: credentials.NewStaticV4("fixture-access", "fixture-secret", ""), Secure: endpoint.secure, Region: "us-east-1"})
		require.NoError(t, err)
		store := &Store{client: client, bucket: "synthetic"}
		requireCreateKind(t, store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size), ArtifactCreateUnsupported)
	}
}

func TestArtifactExclusiveRemoteLostAckReadback(t *testing.T) {
	t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	key, source, digest, size := createFixture(t)
	var mu sync.Mutex
	var object []byte
	var puts int
	store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
			w.Header().Set("Content-Length", fmt.Sprint(len(object)))
			w.Header().Set("ETag", "\"ciphertext\"")
			_, _ = w.Write(object)
			return
		}
		checkConditionalRequest(t, r, key)
		data, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		mu.Lock()
		puts++
		if object != nil {
			mu.Unlock()
			s3Failure(w, 412, "PreconditionFailed")
			return
		}
		object = data
		mu.Unlock()
		connection, _, err := w.(http.Hijacker).Hijack()
		assert.NoError(t, err)
		if err == nil {
			_ = connection.Close()
		}
	})
	requireCreateKind(t, store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size), ArtifactCreateUncertain)
	mu.Lock()
	require.Equal(t, 1, puts)
	require.NotEmpty(t, object)
	mu.Unlock()
	snapshot, err := store.ReadArtifact(context.Background(), key, 1024*1024)
	require.NoError(t, err)
	defer snapshot.Cleanup()
	data, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	require.Equal(t, digest, hex.EncodeToString(hash[:]))
	// Explicit retry remains conditional. Conflict is not evidence of matching
	// content; only the independent logical-byte readback above establishes it.
	requireCreateKind(t, store.CreateArtifactIfAbsent(context.Background(), key, source, digest, size), ArtifactCreateExists)
	mu.Lock()
	require.Equal(t, 2, puts)
	mu.Unlock()
}
func TestArtifactExclusiveRemoteCancellation(t *testing.T) {
	key, source, digest, size := createFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
		checkConditionalRequest(t, r, key)
		close(entered)
		<-release
		w.Header().Set("ETag", "\"synthetic\"")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.CreateArtifactIfAbsent(ctx, key, source, digest, size) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT barrier not reached")
	}
	cancel()
	err := <-done
	requireCreateKind(t, err, ArtifactCreateUncertain)
	require.ErrorIs(t, err, context.Canceled)
	once.Do(func() { close(release) })
	require.ErrorIs(t, store.CreateArtifactIfAbsent(ctx, key, source, digest, size), context.Canceled)
}

func TestArtifactExclusiveRemoteDifferentWriters(t *testing.T) {
	t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	key, first, digest, size := createFixture(t)
	original, err := os.ReadFile(first)
	require.NoError(t, err)
	second := filepath.Join(t.TempDir(), "competing.pdf")
	other := bytes.ReplaceAll(original, []byte("S212_REMOTE_LOGICAL"), []byte("S212_COMPETING_FILE"))
	require.NotEqual(t, original, other)
	require.NoError(t, os.WriteFile(second, other, 0600))
	h := sha256.Sum256(other)
	var mu sync.Mutex
	var stored []byte
	store := exclusiveRemote(t, func(w http.ResponseWriter, r *http.Request) {
		checkConditionalRequest(t, r, key)
		data, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		if stored != nil {
			s3Failure(w, 412, "PreconditionFailed")
			return
		}
		stored = data
		w.Header().Set("ETag", "\"ciphertext\"")
	})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, source := range []struct{ path, digest string }{{first, digest}, {second, hex.EncodeToString(h[:])}} {
		go func() {
			<-start
			results <- store.CreateArtifactIfAbsent(context.Background(), key, source.path, source.digest, size)
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else {
			requireCreateKind(t, err, ArtifactCreateExists)
		}
	}
	require.Equal(t, 1, successes)
	mu.Lock()
	logical, err := decryptData(stored)
	mu.Unlock()
	require.NoError(t, err)
	require.True(t, bytes.Equal(logical, original) || bytes.Equal(logical, other))
}
