package conversion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// A raw filename-derived class or unescaped token text would create active
// elements/attributes and fail these assertions on the actual parsed document.
func TestCodeDocumentCanonicalLanguagesAndEscaping(t *testing.T) {
	source := `</code></pre><script>alert("source")</script><img src="http://127.0.0.1:1/owned"> & ' "`
	for _, tc := range []struct{ name, language string }{
		{"main.go", "go"}, {"main.PY", "python"}, {"main.js", "javascript"},
		{"main.ts", "typescript"}, {"main.cpp", "cpp"}, {"main.html", "html"},
		{"main.json", "json"}, {"main.sh", "bash"}, {"main.txt", "plaintext"},
		{"main.unknown", "plaintext"}, {"main", "plaintext"},
		{`main.go" onmouseover="alert(1)`, "plaintext"},
		{`main.x'><img src='x'>`, "plaintext"}, {`main.go&quot; onload=evil`, "plaintext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document, err := prepareCodeDocument(context.Background(), []byte(source), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			root, err := html.Parse(strings.NewReader(document))
			if err != nil {
				t.Fatal(err)
			}
			var codeText strings.Builder
			var walk func(*html.Node, bool)
			preCount := 0
			walk = func(n *html.Node, inCode bool) {
				if n.Type == html.ElementNode {
					switch n.Data {
					case "script", "img", "iframe", "link", "object":
						t.Errorf("active element %s", n.Data)
					}
					if n.Data == "pre" {
						preCount++
						if len(n.Attr) != 1 || n.Attr[0].Key != "class" || n.Attr[0].Val != "language-"+tc.language {
							t.Errorf("unsafe/noncanonical pre attributes: %v", n.Attr)
						}
					}
					for _, a := range n.Attr {
						if strings.HasPrefix(a.Key, "on") || a.Key == "src" || a.Key == "href" || a.Key == "style" {
							t.Errorf("active attribute %v", a)
						}
					}
					inCode = inCode || n.Data == "code"
				}
				if inCode && n.Type == html.TextNode {
					codeText.WriteString(n.Data)
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, inCode)
				}
			}
			walk(root, false)
			if preCount != 1 || codeText.String() != source {
				t.Fatalf("source changed: pre=%d code=%q", preCount, codeText.String())
			}
		})
	}
}

func TestCodeDocumentHasOfflineSyntaxHighlighting(t *testing.T) {
	doc, err := prepareCodeDocument(context.Background(), []byte("package main\nvar value = 42\n"), "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `<span class="`) {
		t.Fatal("syntax highlighting lost")
	}
	if strings.Contains(doc, "<script") || strings.Contains(doc, "cdnjs") {
		t.Fatal("runtime highlighter dependency")
	}
}

func TestCodeServiceFailureCleansOutput(t *testing.T) {
	markdownChrome(t)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	input := filepath.Join(dir, "input.go")
	if err := os.WriteFile(input, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		output, err := (&ConversionService{}).CodeToPdf(ctx, input, "main.go", PrintOptions{MarginTop: -1})
		cancel()
		if err == nil || output != "" {
			t.Fatalf("failure published %q: %v", output, err)
		}
		assertMarkdownNoArtifacts(t, dir, "input.go")
	}
}

func TestCodeAllBundledLanguagesPreserveSourceText(t *testing.T) {
	source := "// synthetic <tag> & \"quoted\"\nvalue = 42\t# café\n"
	for ext := range codeLanguages {
		t.Run(ext, func(t *testing.T) {
			document, err := prepareCodeDocument(context.Background(), []byte(source), "input."+ext)
			if err != nil {
				t.Fatal(err)
			}
			root, err := html.Parse(strings.NewReader(document))
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var walk func(*html.Node, bool)
			walk = func(n *html.Node, inside bool) {
				inside = inside || n.Type == html.ElementNode && n.Data == "code"
				if inside && n.Type == html.TextNode {
					text.WriteString(n.Data)
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, inside)
				}
			}
			walk(root, false)
			if text.String() != source {
				t.Fatalf("lexer changed source: %q", text.String())
			}
		})
	}
}

func TestCodeFinalHTMLRejectsActiveMarkupAndAlteredPolicy(t *testing.T) {
	for _, fragment := range []string{`<img src="http://127.0.0.1:1/image">`, `<script src="https://example.invalid/script"></script>`, `<iframe src="file:///task-owned-test.html"></iframe>`, `<span onclick="alert(1)">x</span>`, `<span class="unknown">x</span>`, `<style>@import 'https://example.invalid/style';</style>`} {
		if err := validateCodeHTML(context.Background(), codeHTML("plaintext", fragment)); err == nil {
			t.Errorf("validator accepted %s", fragment)
		}
	}
	document := codeHTML("plaintext", "safe")
	for _, altered := range []string{strings.Replace(document, codeStyles, "body{color:red}", 1), strings.Replace(document, `http-equiv="Content-Security-Policy"`, `http-equiv="refresh"`, 1), strings.Replace(document, `<meta charset="UTF-8">`, "", 1)} {
		if err := validateCodeHTML(context.Background(), altered); err == nil {
			t.Fatal("validator accepted altered policy/layout")
		}
	}
}

func TestCodeBoundsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareCodeDocument(ctx, []byte("safe"), "main.go"); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := prepareCodeDocument(context.Background(), []byte(strings.Repeat("<", 5<<20)), "main.txt"); err == nil {
		t.Fatal("HTML expansion budget ignored")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "large.go")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(codeMaxSourceBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := readCodeSource(context.Background(), path); err == nil {
		t.Fatal("source budget ignored")
	}
	if _, err := readCodeSource(ctx, path); err == nil {
		t.Fatal("read cancellation ignored")
	}
	if _, err := readCodeSource(context.Background(), dir); err == nil {
		t.Fatal("nonregular source accepted")
	}
}

func TestCodeBrowserStartupFailureCleansWorkspace(t *testing.T) {
	launcherDir := t.TempDir()
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	launcher := filepath.Join(launcherDir, "headless_shell")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", launcherDir+":/usr/bin:/bin")
	input := filepath.Join(dir, "input.go")
	if err := os.WriteFile(input, []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := (&ConversionService{}).CodeToPdf(context.Background(), input, "input.go", PrintOptions{})
	if err == nil || out != "" {
		t.Fatalf("browser failure published %q: %v", out, err)
	}
	assertMarkdownNoArtifacts(t, dir, "input.go")
}

// Eager/delegating lexers must be interrupted while still tokenizing, not only
// after they return an iterator. This fixture takes seconds without isolation.
func TestCodeActiveCancellationStopsHighlighting(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "worker.pid")
	t.Setenv("PDFNEST_TEST_HIGHLIGHT_PID", marker)
	dir := markdownTemp(t)
	t.Setenv("TMPDIR", dir)
	source := []byte(strings.Repeat("{x}", 10000))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := prepareCodeDocument(ctx, source, "input.svelte")
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("highlight cancellation: elapsed=%v err=%v", time.Since(start), err)
	}
	pidText, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("worker did not start: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(string(pidText))
	if parseErr != nil || pid <= 1 {
		t.Fatalf("invalid worker PID: %q", pidText)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
		t.Fatalf("highlighting worker survived cancellation: %v", err)
	}
	t.Logf("active cancellation returned in %v; worker %d exited", time.Since(start), pid)
	assertMarkdownNoArtifacts(t, dir)
}

func TestCodeWorkerRejectsInvalidInvocation(t *testing.T) {
	if handled, err := RunCodeHighlightWorker(nil); handled || err != nil {
		t.Fatalf("normal invocation intercepted: %v %v", handled, err)
	}
	for _, args := range [][]string{{codeHighlightWorkerCommand}, {codeHighlightWorkerCommand, "evil", filepath.Join(t.TempDir(), "output")}} {
		if handled, err := RunCodeHighlightWorker(args); !handled || err == nil {
			t.Fatalf("invalid worker accepted: %v %v", handled, err)
		}
	}
}
