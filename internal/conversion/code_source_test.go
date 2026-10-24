package conversion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/uploads"
	"testing"
)

func TestCodeSourceRejectsAbove16MiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.go")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((16 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	source, err := readCodeSource(context.Background(), path)
	if err == nil || source != nil {
		t.Fatalf("oversized raw source admitted: read=%d error=%v", len(source), err)
	}
}

// Exercise admission only: exact-limit input need not fit generated HTML.
func TestCodeSourceRawAdmissionBoundary(t *testing.T) {
	for _, size := range []int64{(16 << 20) - 1, 16 << 20, (16 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "boundary.txt")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(size); err != nil {
				file.Close()
				t.Fatal(err)
			}
			file.Close()
			source, err := readCodeSource(context.Background(), path)
			if size > 16<<20 {
				if !errors.Is(err, ErrCodeSourceTooLarge) || source != nil {
					t.Fatalf("oversized: len=%d err=%v", len(source), err)
				}
			} else if err != nil || int64(len(source)) != size {
				t.Fatalf("admission: len=%d want=%d err=%v", len(source), size, err)
			}
		})
	}
}

type codeSourceTestInfo struct {
	os.FileInfo
	size int64
	mode os.FileMode
}

func (i codeSourceTestInfo) Size() int64       { return i.size }
func (i codeSourceTestInfo) Mode() os.FileMode { return i.mode }

type codeObservedReader struct {
	reader io.Reader
	calls  int
	bytes  int64
	cancel context.CancelFunc
}

func (r *codeObservedReader) Read(p []byte) (int, error) {
	r.calls++
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	if r.cancel != nil {
		r.cancel()
	}
	return n, err
}

func TestCodeSourceMetadataRejectsBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.txt")
	if err := os.WriteFile(path, []byte("small"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		size     int64
		mode     os.FileMode
		tooLarge bool
	}{
		{"oversized", (16 << 20) + 1, 0600, true}, {"overflow", math.MaxInt64, 0600, true},
		{"negative", -1, 0600, false}, {"directory", 0, os.ModeDir, false}, {"pipe", 0, os.ModeNamedPipe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &codeObservedReader{reader: strings.NewReader("must not read")}
			source, err := readCodeSourceFrom(context.Background(), reader, codeSourceTestInfo{info, tc.size, tc.mode})
			if source != nil || err == nil || reader.calls != 0 || reader.bytes != 0 {
				t.Fatalf("metadata rejection read contents: source=%d calls=%d bytes=%d err=%v", len(source), reader.calls, reader.bytes, err)
			}
			if tc.tooLarge && !errors.Is(err, ErrCodeSourceTooLarge) {
				t.Fatalf("oversize classification: %v", err)
			}
		})
	}
}

func TestCodeSourceGrowthRemainsBounded(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "growing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// Grow after the validated metadata snapshot, without a nondeterministic race.
	if err := file.Truncate((16 << 20) + 4096); err != nil {
		t.Fatal(err)
	}
	reader := &codeObservedReader{reader: file}
	source, err := readCodeSourceFrom(context.Background(), reader, info)
	if !errors.Is(err, ErrCodeSourceTooLarge) || source != nil || reader.bytes != (16<<20)+1 {
		t.Fatalf("growth limit: source=%d read=%d err=%v", len(source), reader.bytes, err)
	}
}

func TestCodeSourceReadCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.txt")
	if err := os.WriteFile(path, []byte("small"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, alreadyCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &codeObservedReader{reader: strings.NewReader(strings.Repeat("x", 4096)), cancel: cancel}
		if alreadyCanceled {
			cancel()
		}
		source, err := readCodeSourceFrom(ctx, reader, info)
		cancel()
		if !errors.Is(err, context.Canceled) || source != nil || reader.calls > 1 {
			t.Fatalf("canceled read: calls=%d len=%d err=%v", reader.calls, len(source), err)
		}
		if alreadyCanceled && reader.calls != 0 {
			t.Fatal("read after cancellation")
		}
	}
}

func TestCodeSourceFIFORejectedWithoutWaitingForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readCodeSource(context.Background(), path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		// Unblock a regressed blocking open so a failed test leaves no goroutine.
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			writer.Close()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("nonregular source waited for FIFO writer")
	}
}

func TestCodeSourceRejectionStartsNoProcesses(t *testing.T) {
	markers := t.TempDir()
	workerMarker := filepath.Join(markers, "worker.pid")
	browserMarker := filepath.Join(markers, "browser.started")
	t.Setenv("PDFNEST_TEST_HIGHLIGHT_PID", workerMarker)
	launcher := filepath.Join(markers, "headless_shell")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\ntouch '"+browserMarker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", markers+":/usr/bin:/bin")
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	input := filepath.Join(dir, "large.go")
	file, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((16 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	out, err := (&ConversionService{}).CodeToPdf(context.Background(), input, "input.go", PrintOptions{})
	if out != "" || !errors.Is(err, ErrCodeSourceTooLarge) {
		t.Fatalf("rejection: out=%q err=%v", out, err)
	}
	for _, marker := range []string{workerMarker, browserMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("process started on rejected source: %s %v", marker, err)
		}
	}
	assertMarkdownNoArtifacts(t, dir, "large.go")
}

func TestCodeSourceOversizeHTTPClassification(t *testing.T) {
	dir := t.TempDir()
	oversized := filepath.Join(dir, "source.go")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((16 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{oversized, 413, "CODE_SOURCE_TOO_LARGE"}, {filepath.Join(dir, "missing.go"), 500, "CODE_CONVERSION_FAILED"},
	} {
		app := fiber.New()
		ctrl := NewController(&ConversionService{})
		app.Post("/code-to-pdf", func(c *fiber.Ctx) error {
			ctx := uploads.NewContext()
			// Stale/incorrect upload-header sizes cannot override actual file metadata.
			ctx.Add("file", &multipart.FileHeader{Filename: "source.go", Size: 1}, tc.path)
			c.Locals(uploads.LocalKey, ctx)
			return ctrl.ConvertCodeToPDF(c)
		})
		response, err := app.Test(httptest.NewRequest("POST", "/code-to-pdf", nil))
		if err != nil {
			t.Fatal(err)
		}
		var body APIError
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.status || body.Code != tc.code {
			t.Fatalf("HTTP %d body=%+v", response.StatusCode, body)
		}
		if tc.status == 413 && body.Message != "code source exceeds the 16 MiB raw-source limit" {
			t.Fatalf("unsafe/unpredictable oversize message: %q", body.Message)
		}
	}
}

func TestCodeSourceHTMLExpansionBudgetIsIndependent(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "worker.pid")
	t.Setenv("PDFNEST_TEST_HIGHLIGHT_PID", marker)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	// Raw size is only 5 MiB; escaping '<' expands the fragment beyond 16 MiB.
	document, err := prepareCodeDocument(context.Background(), []byte(strings.Repeat("<", 5<<20)), "input.txt")
	if err == nil || errors.Is(err, ErrCodeSourceTooLarge) || document != "" {
		t.Fatalf("HTML expansion classification: document=%d err=%v", len(document), err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("admissible raw source never reached highlighting: %v", err)
	}
	assertMarkdownNoArtifacts(t, dir)
}

// Exercise the real multipart upload middleware with the application's 100 MiB
// HTTP ceiling. The source-specific 16 MiB rejection must originate at the
// controller/service, not from a fake upload context or the transport limit.
func TestCodeSourceOversizeMultipartHTTPContract(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	app := fiber.New(fiber.Config{BodyLimit: 100 << 20})
	controller := NewController(&ConversionService{})
	var observed int64
	app.Post("/api/conversion/code-to-pdf", uploads.Prepare(), func(c *fiber.Ctx) error {
		upload, err := uploads.MustFile(c, "file")
		if err != nil {
			return err
		}
		stat, err := os.Stat(upload.Path)
		if err != nil {
			return err
		}
		observed = stat.Size()
		return controller.ConvertCodeToPDF(c)
	})
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "synthetic.go")
	if err != nil {
		t.Fatal(err)
	}
	size := (16 << 20) + 1
	if _, err := part.Write(bytes.Repeat([]byte("x"), size)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/conversion/code-to-pdf", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response, err := app.Test(request, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result APIError
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if observed != int64(size) || response.StatusCode != 413 || result.Code != "CODE_SOURCE_TOO_LARGE" {
		t.Fatalf("received=%d status=%d result=%+v", observed, response.StatusCode, result)
	}
	t.Logf("received full %d-byte source; HTTP %d code=%s", observed, response.StatusCode, result.Code)
}
