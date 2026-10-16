package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func markdownChrome(t *testing.T) {
	t.Helper()
	for _, executable := range []string{"headless_shell", "chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(executable); err == nil {
			return
		}
	}
	t.Skip("local Chromium not installed; no downloads are permitted")
}

func markdownTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "md-f04-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func markdownCommand(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("PDF assertion tool %s unavailable", name)
	}
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", name, err, output)
	}
	return output
}

func TestMarkdownPDFOfflineContentImagesPaperAndMargins(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	source := "# Offline Heading\n\nParagraph **bold** and *emphasis*.\n\n- first item\n- second item\n\n1. numbered item\n\n[Annotation](https://example.invalid/annotation)\n\n> quoted text\n\n```go\nfmt.Println(\"<safe>\")\n```\n\n| plain | table |\n| --- | --- |\n| cell | value |\n\n![png](" + markdownTestImage(t, "png", 64, 40) + ")\n\n![jpeg](" + markdownTestImage(t, "jpeg", 48, 32) + ")"
	input := filepath.Join(dir, "input.md")
	if err := os.WriteFile(input, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var titleX []float64
	for _, tc := range []struct {
		paper                 string
		width, height, margin float64
	}{
		{"A4", 595.44, 841.68, 0}, {"letter", 612, 792, 1}, {"legal", 612, 1008, 0.25},
	} {
		t.Run(tc.paper, func(t *testing.T) {
			out, err := (&ConversionService{}).MarkdownToPdf(context.Background(), input, PrintOptions{PaperSize: tc.paper, MarginTop: tc.margin, MarginBottom: tc.margin, MarginLeft: tc.margin, MarginRight: tc.margin})
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(out)
			text := string(markdownCommand(t, "pdftotext", out, "-"))
			for _, want := range []string{"Offline Heading", "Paragraph", "first item", "second item", "numbered item", "Annotation", "quoted text", `fmt.Println("<safe>")`, "cell"} {
				if !strings.Contains(text, want) {
					t.Errorf("PDF missing %q: %s", want, text)
				}
			}
			images := string(markdownCommand(t, "pdfimages", "-list", out))
			foundPNG, foundJPEG := false, false
			for _, line := range strings.Split(images, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 5 {
					if fields[3] == "64" && fields[4] == "40" {
						foundPNG = true
					}
					if fields[3] == "48" && fields[4] == "32" {
						foundJPEG = true
					}
				}
			}
			if !foundPNG || !foundJPEG {
				t.Fatalf("missing actual image objects: %s", images)
			}
			info := string(markdownCommand(t, "pdfinfo", out))
			match := regexp.MustCompile(`Page size:\s+([\d.]+) x ([\d.]+)`).FindStringSubmatch(info)
			if len(match) != 3 {
				t.Fatal(info)
			}
			width, _ := strconv.ParseFloat(match[1], 64)
			height, _ := strconv.ParseFloat(match[2], 64)
			if math.Abs(width-tc.width) > 1 || math.Abs(height-tc.height) > 1 {
				t.Fatalf("paper size %fx%f", width, height)
			}
			bbox := string(markdownCommand(t, "pdftotext", "-bbox", out, "-"))
			word := regexp.MustCompile(`<word xMin="([\d.]+)"[^>]*>Offline</word>`).FindStringSubmatch(bbox)
			if len(word) != 2 {
				t.Fatal("heading coordinate unavailable")
			}
			x, _ := strconv.ParseFloat(word[1], 64)
			titleX = append(titleX, x)
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("/URI")) || !bytes.Contains(data, []byte("https://example.invalid/annotation")) {
				t.Fatal("clickable PDF annotation lost")
			}
			stat, _ := os.Stat(out)
			if stat.Mode().Perm() != 0600 {
				t.Fatalf("output permissions %v", stat.Mode())
			}
			if evidence := os.Getenv("PDFNEST_MARKDOWN_EVIDENCE_DIR"); evidence != "" {
				if err := os.WriteFile(filepath.Join(evidence, tc.paper+".pdf"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if len(titleX) == 3 && (math.Abs(titleX[1]-titleX[0]-72) > 1 || math.Abs(titleX[2]-titleX[0]-18) > 1) {
		t.Fatalf("margins did not move actual PDF content: %v", titleX)
	}
	assertMarkdownNoArtifacts(t, dir, "input.md")
}

func TestMarkdownBrowserInMemoryAndStaticLayout(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	doc, err := prepareMarkdownDocument(context.Background(), []byte("# Title\n\n```go\nvar safe = 1\n```"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newMarkdownBrowser(context.Background(), doc.images)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err := b.setDocument(doc.html); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := b.run(chromedp.Evaluate(`[location.href, location.origin, String(document.scripts.length), getComputedStyle(document.querySelector('pre')).backgroundColor, getComputedStyle(document.body).backgroundColor]`, &got)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0] != "about:blank" || got[1] != "null" || got[2] != "0" || got[3] != "rgb(24, 24, 28)" || got[4] != "rgb(30, 30, 36)" {
		t.Fatalf("in-memory/static layout boundary: %v", got)
	}
}

func TestMarkdownJPEGPreservesEXIFOrientation(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	uri := markdownTestImage(t, "jpeg", 64, 40)
	raw, err := base64.StdEncoding.DecodeString(strings.SplitN(uri, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic EXIF orientation 6: display a 64x40 raster rotated to 40x64.
	exif := []byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	oriented := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	oriented = append(oriented, raw[2:]...)
	doc, err := prepareMarkdownDocument(context.Background(), []byte("![oriented](data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(oriented)+")"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newMarkdownBrowser(context.Background(), doc.images)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err = b.setDocument(doc.html); err != nil {
		t.Fatal(err)
	}
	var size []int
	if err = b.run(chromedp.Evaluate("[document.images[0].naturalWidth,document.images[0].naturalHeight]", &size)); err != nil {
		t.Fatal(err)
	}
	if len(size) != 2 || size[0] != 40 || size[1] != 64 {
		t.Fatalf("valid JPEG orientation regressed: %v", size)
	}
}

func TestMarkdownBrowserDeniesNavigationAndAllNetworkPaths(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	var hits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.Redirect(w, r, "/redirected", 302) })
	server := httptest.NewServer(handler)
	defer server.Close()
	tls := httptest.NewTLSServer(handler)
	defer tls.Close()
	file := filepath.Join(t.TempDir(), "owned.html")
	if err := os.WriteFile(file, []byte("TASK OWNED FILE MUST NOT LOAD"), 0600); err != nil {
		t.Fatal(err)
	}
	destinations := []string{server.URL, strings.Replace(server.URL, "127.0.0.1", "localhost", 1), tls.URL, server.URL + "/redirect", "file://" + file, "data:text/html,forbidden", "ftp://127.0.0.1/forbidden"}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err == nil {
		v6 := &http.Server{Handler: handler}
		go v6.Serve(listener)
		defer v6.Close()
		destinations = append(destinations, "http://"+listener.Addr().String())
	} else {
		t.Logf("IPv6 listener unavailable: %v", err)
	}
	for _, destination := range destinations {
		t.Run(destination, func(t *testing.T) {
			b, err := newMarkdownBrowser(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			err = b.run(chromedp.Navigate(destination))
			if err == nil {
				t.Fatal("forbidden direct navigation succeeded")
			}
			if hits.Load() != 0 {
				t.Fatalf("offline browser reached task-owned target: %d", hits.Load())
			}
		})
	}
	for _, expression := range []string{
		fmt.Sprintf(`new Promise((resolve,reject) => { const socket = new WebSocket(%q); socket.onerror = () => reject(new Error('blocked')); socket.onopen = () => resolve('unexpected'); })`, strings.Replace(server.URL, "http:", "ws:", 1)),
		fmt.Sprintf(`new Promise((resolve,reject) => { const socket = new WebSocket(%q); socket.onerror = () => reject(new Error('blocked')); socket.onopen = () => resolve('unexpected'); })`, strings.Replace(tls.URL, "https:", "wss:", 1)),
		fmt.Sprintf(`fetch(%q)`, server.URL),
		fmt.Sprintf(`fetch(%q)`, tls.URL),
	} {
		b, err := newMarkdownBrowser(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.setDocument(markdownHTML("<p>safe</p>")); err != nil {
			b.close()
			t.Fatal(err)
		}
		var ignored any
		err = b.run(chromedp.Evaluate(expression, &ignored, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
		// CSP may synchronously throw before a protocol request exists. Either
		// that exception or our cancellation must reject the attempted operation.
		if err == nil && b.policyError() == nil {
			b.close()
			t.Fatalf("forbidden network operation did not fail: %s", expression)
		}
		b.close()
		if hits.Load() != 0 {
			t.Fatalf("network operation reached target: %d", hits.Load())
		}
	}
}

func TestMarkdownBrowserPolicyFailureCannotPrintSuccessfulPDF(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	b, err := newMarkdownBrowser(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	// Deliberately bypass the AST/final validator to test browser defenses.
	err = b.setDocument(markdownHTML(`<img src="` + server.URL + `/image">`))
	if !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("blocked resource must fail conversion: %v", err)
	}
	var pdf bytes.Buffer
	if err = b.printPDF(&pdf, PrintOptions{}); !errors.Is(err, ErrMarkdownPolicy) || pdf.Len() != 0 {
		t.Fatalf("policy failure printed output: bytes=%d error=%v", pdf.Len(), err)
	}
	if hits.Load() != 0 {
		t.Fatal("blocked image reached server")
	}
}

func TestMarkdownBrowserCancellationCleansProcessAndProxy(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := newMarkdownBrowser(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := chromedp.FromContext(b.ctx).Browser.Process().Pid
	proxyURL := b.proxy.url
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	err = b.run(chromedp.Poll("false", nil, chromedp.WithPollingTimeout(10*time.Second)))
	if err == nil || time.Since(start) > 3*time.Second {
		b.close()
		t.Fatalf("cancellation did not interrupt browser: %v", err)
	}
	b.close()
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
		t.Fatalf("browser process still exists: %v", err)
	}
	client := http.Client{Timeout: time.Second}
	if response, err := client.Get(proxyURL); err == nil {
		response.Body.Close()
		t.Fatal("deny proxy listener leaked")
	}
	assertMarkdownNoArtifacts(t, dir)
}

func TestMarkdownBrowserWorkspaceBudgetCancelsAndCleans(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	b, err := newMarkdownBrowser(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	workspace := b.workspace
	file, err := os.Create(filepath.Join(workspace, "synthetic-spool"))
	if err != nil {
		b.close()
		t.Fatal(err)
	}
	// Sparse file: exercise the logical byte limit without consuming 65 MiB.
	if err = file.Truncate(65 << 20); err != nil {
		file.Close()
		b.close()
		t.Fatal(err)
	}
	file.Close()
	deadline := time.Now().Add(2 * time.Second)
	for b.policyError() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(b.policyError(), ErrMarkdownPolicy) {
		b.close()
		t.Fatal("oversized workspace did not cancel rendering")
	}
	b.close()
	if _, err = os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace was not removed: %v", err)
	}
}

func TestMarkdownWorkspaceMustExistToVerifyBudget(t *testing.T) {
	b := &markdownBrowser{workspace: filepath.Join(t.TempDir(), "missing")}
	if err := b.checkWorkspace(); err == nil {
		t.Fatal("missing workspace was treated as verified")
	}
}

func TestMarkdownServiceFailureRemovesPartialOutput(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	input := filepath.Join(dir, "input.md")
	if err := os.WriteFile(input, []byte("# Failure"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := (&ConversionService{}).MarkdownToPdf(context.Background(), input, PrintOptions{MarginTop: -1})
	if err == nil || out != "" {
		t.Fatalf("invalid print parameters published output %q: %v", out, err)
	}
	assertMarkdownNoArtifacts(t, dir, "input.md")
}

func TestMarkdownDenyProxyNeverForwardsHTTPOrConnect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	p, err := newMarkdownDenyProxy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for _, method := range []string{"GET", "CONNECT"} {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(p.url, "http://"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		address := target.URL
		if method == "CONNECT" {
			address = strings.TrimPrefix(target.URL, "http://")
		}
		fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, address, strings.TrimPrefix(target.URL, "http://"))
		response, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(response, []byte("403 Forbidden")) {
			t.Fatalf("proxy failed closed: %s", response)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("deny proxy forwarded %d requests", hits.Load())
	}
}

func TestMarkdownProxyPreventsImplicitLoopbackBypass(t *testing.T) {
	markdownChrome(t)
	t.Setenv("TMPDIR", markdownTemp(t))
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.Redirect(w, r, "/redirected", 302) }))
	defer target.Close()
	for _, address := range []string{target.URL, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)} {
		// Intentionally permit this URL at interception and omit CSP to isolate
		// the proxy/bypass configuration as an independent layer of enforcement.
		b, err := newMarkdownBrowser(context.Background(), map[string]struct{}{address: {}})
		if err != nil {
			t.Fatal(err)
		}
		err = b.setDocument(`<html><body><img src="` + address + `"></body></html>`)
		if !errors.Is(err, ErrMarkdownPolicy) {
			b.close()
			t.Fatalf("proxy denial did not fail conversion: %v", err)
		}
		b.close()
		if hits.Load() != 0 {
			t.Fatalf("Chromium bypassed deny proxy for loopback: %d", hits.Load())
		}
	}
}

func assertMarkdownNoArtifacts(t *testing.T, dir string, allowed ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		ok := false
		for _, name := range allowed {
			if entry.Name() == name {
				ok = true
			}
		}
		if !ok {
			t.Errorf("temporary artifact leaked: %s", entry.Name())
		}
	}
}
