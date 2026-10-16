package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func markdownTestImage(t *testing.T, format string, width, height int) string {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: 190, G: uint8(x % 255), B: uint8(y % 255), A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buf, im, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&buf, im)
	}
	if err != nil {
		t.Fatal(err)
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestMarkdownImagePolicyResolvesAllReferenceForms(t *testing.T) {
	for _, destination := range []string{
		"http://127.0.0.1/image", "http://[::1]/image", "http://localhost/image",
		"http://10.0.0.1/image", "http://169.254.169.254/image", "https://example.invalid/image",
		"//localhost/image", "image.png", "../image.png", "/tmp/image.png", "file:///tmp/image.png",
		"ftp://localhost/image", "blob:untrusted", "javascript:alert%281%29", "", "http&#58;//localhost/image",
		"http\\://localhost/image", "data:image/svg+xml;base64,PHN2Zy8+", "d%61ta:image/png;base64,AA==",
	} {
		for _, form := range []string{
			"![alt](%s)", "![alt][ref]\n\n[ref]: %s", "![ref][]\n\n[ref]: %s", "![ref]\n\n[ref]: %s",
		} {
			// An empty reference definition is unresolved literal text, not an image.
			if destination == "" && form != "![alt](%s)" {
				continue
			}
			md := strings.Replace(form, "%s", destination, 1)
			t.Run(md, func(t *testing.T) {
				_, err := prepareMarkdownDocument(context.Background(), []byte(md))
				if !errors.Is(err, ErrMarkdownPolicy) {
					t.Fatalf("forbidden image must fail explicitly: %v", err)
				}
			})
		}
	}
}

func TestMarkdownEmbeddedRasterValidation(t *testing.T) {
	pngURI := markdownTestImage(t, "png", 64, 40)
	jpegURI := markdownTestImage(t, "jpeg", 48, 32)
	for _, uri := range []string{pngURI, jpegURI, strings.Replace(pngURI, "data:", "data&#58;", 1), strings.Replace(pngURI, "data:", "data\\:", 1)} {
		doc, err := prepareMarkdownDocument(context.Background(), []byte("# Embedded\n\n![valid]("+uri+")"))
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.images) != 1 || !strings.Contains(doc.html, "<h1>Embedded</h1>") {
			t.Fatalf("missing validated content: %s", doc.html)
		}
		for canonical := range doc.images {
			comma := strings.IndexByte(canonical, ',')
			raw, err := base64.StdEncoding.DecodeString(canonical[comma+1:])
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := image.Decode(bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, uri := range []string{
		"data:image/png;base64,AA==", "data:image/png;base64,invalid!", "data:image/gif;base64,R0lGODlh",
		strings.Replace(pngURI, "image/png", "image/jpeg", 1),
		strings.Replace(pngURI, ";base64,", ";charset=utf-8;base64,", 1),
		pngURI[:len(pngURI)-16], "data:image/png,%89PNG", "data:image/svg+xml;base64,PHN2Zy8+",
	} {
		if _, err := prepareMarkdownDocument(context.Background(), []byte("![invalid]("+uri+")")); !errors.Is(err, ErrMarkdownPolicy) {
			t.Fatalf("accepted invalid image %q: %v", uri[:min(60, len(uri))], err)
		}
	}
}

func TestMarkdownResourceBudgets(t *testing.T) {
	ctx := context.Background()
	if _, err := prepareMarkdownDocument(ctx, bytes.Repeat([]byte("x"), (2<<20)+1)); !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("oversized source: %v", err)
	}
	uri := markdownTestImage(t, "png", 1, 1)
	if _, err := prepareMarkdownDocument(ctx, []byte(strings.Repeat("![x]("+uri+")\n", 32))); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareMarkdownDocument(ctx, []byte(strings.Repeat("![x]("+uri+")\n", 33))); !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("image count: %v", err)
	}
	oversized := "![x](data:image/png;base64," + strings.Repeat("A", (1<<20)+4) + ")"
	if _, err := prepareMarkdownDocument(ctx, []byte(oversized)); !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("encoded bytes: %v", err)
	}
	// Only the header is valid: the dimension limit must reject it before pixel decoding.
	for _, size := range [][2]uint32{{4097, 1}, {2049, 2048}} {
		raw, _ := base64.StdEncoding.DecodeString(strings.SplitN(uri, ",", 2)[1])
		binary.BigEndian.PutUint32(raw[16:20], size[0])
		binary.BigEndian.PutUint32(raw[20:24], size[1])
		binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
		_, err := prepareMarkdownDocument(ctx, []byte("![x](data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw)+")"))
		if !errors.Is(err, ErrMarkdownPolicy) || !strings.Contains(err.Error(), "dimensions") {
			t.Fatalf("dimensions must fail before decode: %v", err)
		}
	}
	large := markdownTestImage(t, "png", 2000, 2000)
	if _, err := prepareMarkdownDocument(ctx, []byte(strings.Repeat("![x]("+large+")\n", 5))); !errors.Is(err, ErrMarkdownPolicy) || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate pixels: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := prepareMarkdownDocument(canceled, []byte("# canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestMarkdownFinalHTMLRejectsResourceConstructs(t *testing.T) {
	for _, fragment := range []string{
		`<img src="http://localhost/x">`, `<img src="file:///tmp/x">`, `<img srcset="data:image/png;base64,AA==">`,
		`<a href="https://example.invalid" ping="http://localhost">link</a>`, `<p style="background:url(file:///tmp/x)">x</p>`,
		`<script>new WebSocket('ws://localhost')</script>`, `<style>@import 'http://localhost/x';</style>`,
		`<link rel=stylesheet href=http://localhost/x>`, `<base href=file:///tmp/>`, `<meta http-equiv=refresh content="0;url=http://localhost">`,
		`<iframe srcdoc="<img src=file:///tmp/x>"></iframe>`, `<svg><image href=file:///tmp/x /></svg>`, `<object data=file:///tmp/x>`,
		`<p onclick="fetch('http://localhost')">x</p>`, `<form action=http://localhost><input type=image src=file:///tmp/x></form>`,
		`<video poster=file:///tmp/x></video>`, `<math href=http://localhost></math>`,
	} {
		if err := validateMarkdownHTML(markdownHTML(fragment), nil); !errors.Is(err, ErrMarkdownPolicy) {
			t.Fatalf("unsafe final HTML accepted %s: %v", fragment, err)
		}
	}
	// Ordinary links are annotations, not render-time loads; code remains escaped.
	doc, err := prepareMarkdownDocument(context.Background(), []byte("# Title\n\n- one\n- two\n\n[link](https://example.invalid)\n\n```go\nfmt.Println(\"<img src='http://localhost'>\")\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.html, `href="https://example.invalid"`) || !strings.Contains(doc.html, "&lt;img") {
		t.Fatal("lost link or escaped code")
	}
}

func TestMarkdownCommentsRemainInert(t *testing.T) {
	for _, source := range []string{"# Visible\n\n<!-- explanation -->\n\nParagraph", "# Visible\n\nText <!-- explanation --> continues"} {
		doc, err := prepareMarkdownDocument(context.Background(), []byte(source))
		if err != nil {
			t.Fatalf("harmless Markdown comment regressed: %v", err)
		}
		if len(doc.images) != 0 || !strings.Contains(doc.html, "<h1>Visible</h1>") {
			t.Fatal("comment changed visible Markdown")
		}
	}
	for _, source := range []string{`<img src="http://localhost/image">`, `<script>fetch('http://localhost')</script>`, `<!-- comment --><img src="http://localhost/image">`} {
		if _, err := prepareMarkdownDocument(context.Background(), []byte(source)); !errors.Is(err, ErrMarkdownPolicy) {
			t.Fatalf("raw HTML resource did not fail explicitly: %v", err)
		}
	}
}

func TestMarkdownGeneratedBufferCannotBypassByteBudget(t *testing.T) {
	buffer := &markdownBoundedBuffer{ctx: context.Background(), limit: 4}
	if _, err := io.WriteString(buffer, "12345"); !errors.Is(err, ErrMarkdownPolicy) || buffer.Len() != 0 {
		t.Fatalf("string write bypassed byte budget: bytes=%d error=%v", buffer.Len(), err)
	}
}

func TestMarkdownSourceReadIsBoundedAndCancelable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.md")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate((2 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err = readMarkdownSource(context.Background(), path); !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("oversized source read: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = readMarkdownSource(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source read: %v", err)
	}
}

func TestMarkdownPDFByteBudgetAndShortWrites(t *testing.T) {
	oversized := make([]byte, (32<<20)+1)
	copy(oversized, "%PDF-")
	var output bytes.Buffer
	if _, err := writeMarkdownPDFChunk(&output, oversized, 0); !errors.Is(err, ErrMarkdownPolicy) || output.Len() != 0 {
		t.Fatalf("oversized PDF was written: bytes=%d error=%v", output.Len(), err)
	}
	if count, err := writeMarkdownPDFChunk(io.Discard, oversized[:32<<20], 0); err != nil || count != 32<<20 {
		t.Fatalf("valid budget boundary: bytes=%d error=%v", count, err)
	}
	if _, err := writeMarkdownPDFChunk(&output, []byte("x"), 32<<20); !errors.Is(err, ErrMarkdownPolicy) || output.Len() != 0 {
		t.Fatalf("aggregate PDF budget bypass: %v", err)
	}
	if _, err := writeMarkdownPDFChunk(markdownShortWriter{}, []byte("%PDF-1.7"), 0); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write was ignored: %v", err)
	}
}

type markdownShortWriter struct{}

func (markdownShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
