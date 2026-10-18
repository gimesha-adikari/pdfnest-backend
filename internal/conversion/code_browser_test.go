package conversion

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// Observe actual renderer requests, source text, and token colors. Escaping
// alone must not regress highlighting, and neither source nor filename may
// create resource-bearing DOM elements or run scripts.
func TestCodeBrowserEscapesInjectionAndMakesNoResourceRequests(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	local := filepath.Join(dir, "owned.html")
	if err := os.WriteFile(local, []byte("TASK OWNED SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded := strings.NewReplacer("/", "&#47;", ".", "&#46;")
	source := `package main
// literal <script>window.injected=true</script>
var text = "<img src='` + server.URL + `/image'>"
// https://example.invalid/remote file://` + local + `
`
	for _, filename := range []string{"main.go", "main.unknown", `main.x"><img src="` + encoded.Replace(server.URL) + `&#47;injected">`, `main.x"><iframe src="file:` + encoded.Replace(local) + `"></iframe>`} {
		t.Run(filename, func(t *testing.T) {
			doc, err := prepareCodeDocument(context.Background(), []byte(source), filename)
			if err != nil {
				t.Fatal(err)
			}
			b, err := newMarkdownBrowser(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			var mu sync.Mutex
			var requests []string
			chromedp.ListenTarget(b.ctx, func(event any) {
				if e, ok := event.(*network.EventRequestWillBeSent); ok && e.Request.URL != "about:blank" {
					mu.Lock()
					requests = append(requests, e.Request.URL)
					mu.Unlock()
				}
			})
			if err := b.setDocument(doc); err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := b.run(chromedp.Evaluate(`[location.href, String(document.scripts.length), String(document.querySelectorAll('img,iframe,object,link').length), document.querySelector('code').textContent, String(window.injected), getComputedStyle(document.querySelector('.token-keyword') || document.querySelector('code')).color]`, &got)); err != nil {
				t.Fatal(err)
			}
			if len(got) != 6 || got[0] != "about:blank" || got[1] != "0" || got[2] != "0" || got[3] != source || got[4] != "undefined" {
				t.Fatalf("injection/source boundary: %q", got)
			}
			if filename == "main.go" && got[5] != "rgb(204, 153, 205)" {
				t.Fatalf("highlighting missing: %v", got)
			}
			var pdf bytes.Buffer
			if err := b.printPDF(&pdf, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := b.sealPolicyEvents(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			observed := append([]string(nil), requests...)
			mu.Unlock()
			if len(observed) != 0 || hits.Load() != 0 {
				t.Fatalf("unwanted access: CDP=%v fixture=%d", observed, hits.Load())
			}
			t.Logf("document requests=%d loopback hits=%d PDF bytes=%d", len(observed), hits.Load(), pdf.Len())
		})
	}
}

func TestCodePDFValidTextPaperMarginsAndPagination(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	input := filepath.Join(dir, "input.go")
	source := "package main\n\n// Literal <script>alert(1)</script> & quoted text\nvar safe = 42\n"
	if err := os.WriteFile(input, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var positions []float64
	for _, tc := range []struct {
		paper                 string
		width, height, margin float64
	}{{"A4", 595.44, 841.68, 0}, {"letter", 612, 792, 1}, {"legal", 612, 1008, 0.25}} {
		t.Run(tc.paper, func(t *testing.T) {
			out, err := (&ConversionService{}).CodeToPdf(context.Background(), input, "main.go", PrintOptions{PaperSize: tc.paper, MarginTop: tc.margin, MarginBottom: tc.margin, MarginLeft: tc.margin, MarginRight: tc.margin})
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(out)
			if err := validateMarkdownPDFFile(out); err != nil {
				t.Fatal(err)
			}
			count, err := api.PageCountFile(out)
			if err != nil || count != 1 {
				t.Fatalf("pages=%d err=%v", count, err)
			}
			text := string(markdownCommand(t, "pdftotext", out, "-"))
			for _, want := range []string{"package main", "<script>alert(1)</script>", "var safe = 42"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q: %s", want, text)
				}
			}
			info := string(markdownCommand(t, "pdfinfo", out))
			match := regexp.MustCompile(`Page size:\s+([\d.]+) x ([\d.]+)`).FindStringSubmatch(info)
			if len(match) != 3 {
				t.Fatal(info)
			}
			width, _ := strconv.ParseFloat(match[1], 64)
			height, _ := strconv.ParseFloat(match[2], 64)
			if math.Abs(width-tc.width) > 1 || math.Abs(height-tc.height) > 1 {
				t.Fatalf("paper=%fx%f", width, height)
			}
			bbox := string(markdownCommand(t, "pdftotext", "-bbox", out, "-"))
			word := regexp.MustCompile(`<word xMin="([\d.]+)"[^>]*>package</word>`).FindStringSubmatch(bbox)
			if len(word) != 2 {
				t.Fatal("code coordinate unavailable")
			}
			x, _ := strconv.ParseFloat(word[1], 64)
			positions = append(positions, x)
			stat, err := os.Stat(out)
			if err != nil || stat.Mode().Perm() != 0600 {
				t.Fatalf("output permission: %v %v", stat, err)
			}
		})
	}
	if len(positions) == 3 && (math.Abs(positions[1]-positions[0]-72) > 1 || math.Abs(positions[2]-positions[0]-18) > 1) {
		t.Fatalf("margins did not move content: %v", positions)
	}
	if err := os.WriteFile(input, []byte(strings.Repeat("// synthetic long source line\n", 180)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := (&ConversionService{}).CodeToPdf(context.Background(), input, "main.txt", PrintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	count, err := api.PageCountFile(out)
	os.Remove(out)
	if err != nil || count < 2 {
		t.Fatalf("pagination pages=%d error=%v", count, err)
	}
	assertMarkdownNoArtifacts(t, dir, "input.go")
}

// Bypass the final Code validator deliberately. Browser enforcement must still
// deny attempted resources/navigation and never publish PDF on policy failure.
func TestCodeBrowserPolicyFailureCannotPublishPDF(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	local := filepath.Join(dir, "owned.html")
	if err := os.WriteFile(local, []byte("TASK OWNED FILE"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`<img src="` + server.URL + `/image">`, `<link rel="stylesheet" href="https://example.invalid/remote.css">`,
		`<iframe src="` + server.URL + `/frame"></iframe>`,
		`<meta http-equiv="refresh" content="0;url=` + server.URL + `/navigate">`,
	} {
		t.Run(fragment, func(t *testing.T) {
			b, err := newMarkdownBrowser(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			setErr := b.setDocument(codeHTML("plaintext", fragment))
			file, err := os.CreateTemp(dir, "code-compiled-*.pdf")
			if err != nil {
				t.Fatal(err)
			}
			path := file.Name()
			out, err := completeMarkdownPDFOutput(context.Background(), file, func(writer io.Writer) error { return b.printPDF(writer, PrintOptions{}) }, b.sealPolicyEvents)
			if !errors.Is(setErr, ErrMarkdownPolicy) || !errors.Is(err, ErrMarkdownPolicy) || out != "" {
				t.Fatalf("unsafe artifact: set=%v print=%v output=%q", setErr, err, out)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("partial output leaked: %v", err)
			}
		})
	}
	b, err := newMarkdownBrowser(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.run(chromedp.Navigate("file://" + local)); !errors.Is(err, ErrMarkdownPolicy) {
		b.close()
		t.Fatalf("local navigation did not fail policy: %v", err)
	}
	b.close()
	assertMarkdownNoArtifacts(t, dir, "owned.html")
	if hits.Load() != 0 {
		t.Fatalf("fixture received %d requests", hits.Load())
	}
}
