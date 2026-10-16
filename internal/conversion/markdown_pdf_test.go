package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/cdproto/page"
)

func TestMarkdownPDFStreamAcceptsChunkBoundariesAndBase64(t *testing.T) {
	pdf := minimalMarkdownTestPDF()
	tests := []struct {
		name   string
		chunks []markdownPDFStreamChunk
	}{
		{
			name:   "single chunk",
			chunks: []markdownPDFStreamChunk{{Data: string(pdf), EOF: true}},
		},
		{
			name: "header split across chunks",
			chunks: []markdownPDFStreamChunk{
				{Data: "%P"}, {Data: "DF"}, {Data: "-"}, {Data: string(pdf[5:]), EOF: true},
			},
		},
		{
			name:   "single byte chunks",
			chunks: markdownPDFByteChunks(pdf),
		},
		{
			name:   "base64 encoded chunks",
			chunks: []markdownPDFStreamChunk{{Data: base64.StdEncoding.EncodeToString(pdf), Base64Encoded: true, EOF: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, path, err := completeMarkdownTestPDF(context.Background(), t.TempDir(), tc.chunks, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]byte(nil), pdf...)
			if bytes.Equal(want[:8], []byte("%PDF-1.4")) {
				copy(want[:8], "%PDF-1.5")
			}
			if output != path || !bytes.Equal(got, want) {
				t.Fatal("valid PDF bytes or published path changed across chunking")
			}
		})
	}
}

func TestMarkdownPDFValidationAcceptsTrailingWhitespace(t *testing.T) {
	data := append(minimalMarkdownTestPDF(), []byte("\n \t\r\n")...)
	path := writeMarkdownTestFile(t, data)
	if err := validateMarkdownPDFFile(path); err != nil {
		t.Fatalf("strict validation rejected permitted trailing whitespace: %v", err)
	}
}

func TestMarkdownPDFVersionPromotionChangesOnlyTheHeader(t *testing.T) {
	want := minimalMarkdownTestPDF()
	path := writeMarkdownTestFile(t, want)
	if err := normalizeChromiumMarkdownPDFVersion(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copy(want[:8], "%PDF-1.5")
	if !bytes.Equal(got, want) {
		t.Fatal("PDF version promotion changed bytes outside its declaration")
	}
}

func TestMarkdownPDFPublicationRejectsIncompleteOrMalformedFiles(t *testing.T) {
	valid := minimalMarkdownTestPDF()
	truncatedXRef := append([]byte(nil), valid...)
	index := bytes.Index(truncatedXRef, []byte("xref\n0 "))
	if index < 0 {
		t.Fatal("test PDF has no classic xref table")
	}
	truncatedXRef = truncatedXRef[:index+len("xref\n0 ")]

	badRoot := bytes.Replace(valid, []byte("/Root 1 0 R"), []byte("/Root 9 0 R"), 1)
	badPageTree := bytes.Replace(valid, []byte("/Kids [3 0 R] /Count 1"), []byte("/Kids [99 0 R] /Count 1"), 1)
	object1Offset := bytes.Index(valid, []byte("1 0 obj\n"))
	badXRef := bytes.Replace(valid,
		[]byte(fmt.Sprintf("%010d 00000 n \n", object1Offset)),
		[]byte("0000000001 00000 n \n"), 1)
	xrefOffset := bytes.Index(valid, []byte("\nxref\n")) + 1
	if xrefOffset <= 0 {
		t.Fatal("test PDF has no xref table target")
	}
	badStartXRef := bytes.Replace(valid,
		[]byte(fmt.Sprintf("startxref\n%d\n", xrefOffset)),
		[]byte("startxref\n0\n"), 1)
	if bytes.Equal(badXRef, valid) || bytes.Equal(badStartXRef, valid) {
		t.Fatal("test PDF corruption fixture was not applied")
	}
	missingEOF := bytes.TrimSuffix(valid, []byte("%%EOF\n"))

	tests := []struct {
		name   string
		chunks []markdownPDFStreamChunk
	}{
		{name: "empty stream", chunks: []markdownPDFStreamChunk{{EOF: true}}},
		{name: "only PDF header", chunks: []markdownPDFStreamChunk{{Data: "%PDF-", EOF: true}}},
		{name: "premature protocol EOF", chunks: []markdownPDFStreamChunk{{Data: "%PDF-1.4"}}},
		{name: "missing PDF EOF marker", chunks: []markdownPDFStreamChunk{{Data: string(missingEOF), EOF: true}}},
		{name: "truncated cross reference", chunks: []markdownPDFStreamChunk{{Data: string(truncatedXRef), EOF: true}}},
		{name: "corrupt cross reference entry", chunks: []markdownPDFStreamChunk{{Data: string(badXRef), EOF: true}}},
		{name: "misdirected startxref", chunks: []markdownPDFStreamChunk{{Data: string(badStartXRef), EOF: true}}},
		{name: "invalid trailer root", chunks: []markdownPDFStreamChunk{{Data: string(badRoot), EOF: true}}},
		{name: "invalid page tree", chunks: []markdownPDFStreamChunk{{Data: string(badPageTree), EOF: true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			output, path, err := completeMarkdownTestPDF(context.Background(), dir, tc.chunks, func() error { return nil })
			if err == nil || output != "" || path == "" {
				t.Fatalf("malformed PDF was published: output=%q path=%q err=%v", output, path, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed PDF output still exists: %v", statErr)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("failed PDF output was not cleaned up: %v", entries)
			}
		})
	}
}

func TestMarkdownPDFStreamRejectsMalformedBase64(t *testing.T) {
	for _, value := range []string{"JVBERi0", "JVBERi0=\n"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			var output bytes.Buffer
			err := readMarkdownPDFStream(context.Background(), &output, markdownPDFChunks(markdownPDFStreamChunk{
				Data: value, Base64Encoded: true, EOF: true,
			}))
			if err == nil {
				t.Fatal("malformed or incomplete base64 was accepted")
			}
		})
	}
}

func TestMarkdownPDFStreamEnforcesAggregateLimit(t *testing.T) {
	block := bytes.Repeat([]byte{'x'}, 1<<20)
	block[0], block[1], block[2], block[3], block[4] = '%', 'P', 'D', 'F', '-'
	blockText := string(block)
	chunks := make([]markdownPDFStreamChunk, 33)
	for i := range chunks {
		chunks[i] = markdownPDFStreamChunk{Data: blockText, EOF: i == len(chunks)-1}
	}
	err := readMarkdownPDFStream(context.Background(), io.Discard, markdownPDFChunks(chunks...))
	if !errors.Is(err, ErrMarkdownPolicy) {
		t.Fatalf("32 MiB aggregate output limit was not enforced: %v", err)
	}
}

type markdownFailingWriter struct {
	err error
	n   int
}

func (w markdownFailingWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.n, nil
}

func TestMarkdownPDFStreamPropagatesWriterFailures(t *testing.T) {
	for name, writer := range map[string]io.Writer{
		"writer error": markdownFailingWriter{err: errors.New("disk full")},
		"short write":  markdownFailingWriter{n: 2},
	} {
		t.Run(name, func(t *testing.T) {
			err := readMarkdownPDFStream(context.Background(), writer, markdownPDFChunks(markdownPDFStreamChunk{Data: "%PDF-1.4", EOF: true}))
			if err == nil {
				t.Fatal("writer failure was hidden")
			}
		})
	}
}

func TestMarkdownPDFStreamCancellationAtCompletionFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	reader := func(context.Context) (markdownPDFStreamChunk, error) {
		cancel()
		return markdownPDFStreamChunk{Data: string(minimalMarkdownTestPDF()), EOF: true}, nil
	}
	err := readMarkdownPDFStream(ctx, &output, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation at EOF was reported as success: %v", err)
	}
}

func TestMarkdownPolicyViolationAtPDFCompletionCannotPublish(t *testing.T) {
	dir := t.TempDir()
	file, err := os.CreateTemp(dir, "md-policy-boundary-*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	defer cancelBrowser()
	browser := &markdownBrowser{ctx: browserCtx, cancel: cancelBrowser}
	chunks := []markdownPDFStreamChunk{{Data: string(minimalMarkdownTestPDF()), EOF: true}}
	boundary := func() error {
		started := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		go func() {
			close(started)
			<-release
			browser.handleEvent(&page.EventWindowOpen{})
			close(done)
		}()
		<-started
		close(release)
		<-done
		return browser.policyError()
	}
	output, err := completeMarkdownPDFOutput(context.Background(), file, func(writer io.Writer) error {
		return readMarkdownPDFStream(context.Background(), writer, markdownPDFChunks(chunks...))
	}, boundary)
	if !errors.Is(err, ErrMarkdownPolicy) || output != "" {
		t.Fatalf("completion-boundary policy event allowed publication: output=%q err=%v", output, err)
	}
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Fatalf("policy failure left a publishable PDF behind: %v", err)
	}
}

func markdownPDFChunks(chunks ...markdownPDFStreamChunk) markdownPDFChunkReader {
	index := 0
	return func(context.Context) (markdownPDFStreamChunk, error) {
		if index >= len(chunks) {
			return markdownPDFStreamChunk{}, io.EOF
		}
		chunk := chunks[index]
		index++
		return chunk, nil
	}
}

func markdownPDFByteChunks(data []byte) []markdownPDFStreamChunk {
	chunks := make([]markdownPDFStreamChunk, len(data))
	for i, value := range data {
		chunks[i] = markdownPDFStreamChunk{Data: string([]byte{value}), EOF: i == len(data)-1}
	}
	return chunks
}

func completeMarkdownTestPDF(ctx context.Context, dir string, chunks []markdownPDFStreamChunk, boundary func() error) (string, string, error) {
	file, err := os.CreateTemp(dir, "md-test-*.pdf")
	if err != nil {
		return "", "", err
	}
	path := file.Name()
	output, err := completeMarkdownPDFOutput(ctx, file, func(writer io.Writer) error {
		return readMarkdownPDFStream(ctx, writer, markdownPDFChunks(chunks...))
	}, boundary)
	return output, path, err
}

func writeMarkdownTestFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalMarkdownTestPDF() []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xrefOffset := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return output.Bytes()
}
