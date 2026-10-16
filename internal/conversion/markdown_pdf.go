package conversion

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const markdownPDFHeader = "%PDF-"

var markdownPDFXRefStreamType = regexp.MustCompile(`/Type[\x00\t\n\f\r ]*/XRef(?:[\x00\t\n\f\r />\[])`)

type markdownPDFStreamChunk struct {
	Data          string
	Base64Encoded bool
	EOF           bool
}

type markdownPDFChunkReader func(context.Context) (markdownPDFStreamChunk, error)

// readMarkdownPDFStream copies a bounded protocol stream without retaining the
// complete document in memory. It validates the header across chunk boundaries;
// the completed file is structurally validated separately before publication.
func readMarkdownPDFStream(ctx context.Context, writer io.Writer, read markdownPDFChunkReader) error {
	if writer == nil || read == nil {
		return fmt.Errorf("missing PDF stream reader or writer")
	}

	var header [len(markdownPDFHeader)]byte
	headerSize := 0
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := read(ctx)
		if err != nil {
			return err
		}

		data := []byte(chunk.Data)
		if chunk.Base64Encoded {
			if strings.ContainsAny(chunk.Data, "\r\n") {
				return fmt.Errorf("browser returned malformed base64 PDF data")
			}
			data, err = base64.StdEncoding.Strict().DecodeString(chunk.Data)
			if err != nil {
				return fmt.Errorf("decoding browser PDF stream: %w", err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(data) == 0 && !chunk.EOF {
			return fmt.Errorf("browser returned an empty PDF stream chunk")
		}

		total, err = writeMarkdownPDFChunk(writer, data, total)
		if err != nil {
			return err
		}
		if headerSize < len(header) {
			n := copy(header[headerSize:], data)
			headerSize += n
			if headerSize == len(header) && !bytes.Equal(header[:], []byte(markdownPDFHeader)) {
				return fmt.Errorf("browser returned an invalid PDF stream header")
			}
		}

		if chunk.EOF {
			if err := ctx.Err(); err != nil {
				return err
			}
			if total == 0 {
				return fmt.Errorf("browser returned an empty PDF stream")
			}
			if headerSize < len(header) {
				return fmt.Errorf("browser returned a truncated PDF header")
			}
			return nil
		}
	}
}

func writeMarkdownPDFChunk(writer io.Writer, data []byte, total int) (int, error) {
	if total < 0 || len(data) > markdownMaxPDFBytes-total {
		return total, markdownPolicyError("PDF exceeds 32 MiB")
	}
	written, err := writer.Write(data)
	if written < 0 || written > len(data) {
		return total, fmt.Errorf("PDF writer returned an invalid byte count")
	}
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return total + written, err
}

// validateMarkdownPDFFile requires a non-empty bounded PDF with a strict
// cross-reference, trailer, catalog and page tree, then confirms it has pages.
func validateMarkdownPDFFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat generated PDF: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > markdownMaxPDFBytes {
		return fmt.Errorf("generated PDF is missing, empty, or exceeds 32 MiB")
	}
	if err := normalizeChromiumMarkdownPDFVersion(path); err != nil {
		return err
	}
	if err := validateMarkdownPDFXRefPointer(path, info.Size()); err != nil {
		return err
	}

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationStrict
	conf.Optimize = false
	if err := api.ValidateFile(path, conf); err != nil {
		return fmt.Errorf("generated PDF failed strict structural validation: %w", err)
	}
	pageCount, err := api.PageCountFile(path)
	if err != nil {
		return fmt.Errorf("reading generated PDF page count: %w", err)
	}
	if pageCount < 1 {
		return fmt.Errorf("generated PDF has no pages")
	}
	return nil
}

// Chromium currently emits a 1.4 header while using the /Tabs page-tree entry,
// which is defined for PDF 1.5 and later. Raise only that declaration to 1.5
// before strict validation; the PDF body and xref offsets remain unchanged.
func normalizeChromiumMarkdownPDFVersion(path string) (err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening generated PDF header: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing generated PDF header: %w", closeErr)
		}
	}()

	var header [8]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return fmt.Errorf("reading generated PDF header: %w", err)
	}
	if bytes.Equal(header[:], []byte("%PDF-1.4")) {
		copy(header[:], "%PDF-1.5")
		written, writeErr := file.WriteAt(header[:], 0)
		if writeErr != nil {
			return fmt.Errorf("updating Chromium PDF version declaration: %w", writeErr)
		}
		if written != len(header) {
			return io.ErrShortWrite
		}
	}
	return nil
}

// validateMarkdownPDFXRefPointer rejects pdfcpu's recovery path for a missing
// or misdirected final startxref. pdfcpu performs the full xref, trailer,
// catalog and page-tree parse below; this bounded check only proves that the
// terminal pointer names the actual classic table or xref-stream object.
func validateMarkdownPDFXRefPointer(path string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening generated PDF xref: %w", err)
	}
	defer file.Close()

	eofLast, err := markdownPDFLastNonWhitespace(file, size)
	if err != nil {
		return err
	}
	const eofMarker = "%%EOF"
	if eofLast < int64(len(eofMarker)-1) {
		return fmt.Errorf("generated PDF has no complete EOF marker")
	}
	eofStart := eofLast - int64(len(eofMarker)-1)
	var marker [len(eofMarker)]byte
	if _, err := file.ReadAt(marker[:], eofStart); err != nil || !bytes.Equal(marker[:], []byte(eofMarker)) {
		return fmt.Errorf("generated PDF has no complete EOF marker")
	}

	startXRef, err := markdownPDFLastSequence(file, eofStart, []byte("startxref"))
	if err != nil {
		return fmt.Errorf("generated PDF has no startxref pointer: %w", err)
	}
	reader := bufio.NewReader(io.NewSectionReader(file, startXRef+int64(len("startxref")), eofStart-(startXRef+int64(len("startxref")))))
	for {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			return fmt.Errorf("generated PDF startxref value is missing")
		}
		if !markdownPDFWhitespace(b) {
			if b < '0' || b > '9' {
				return fmt.Errorf("generated PDF startxref value is malformed")
			}
			if err := reader.UnreadByte(); err != nil {
				return err
			}
			break
		}
	}
	var offsetText strings.Builder
	for {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			return fmt.Errorf("generated PDF startxref value is unterminated")
		}
		if b >= '0' && b <= '9' {
			if offsetText.Len() >= 20 {
				return fmt.Errorf("generated PDF startxref value is too large")
			}
			offsetText.WriteByte(b)
			continue
		}
		if !markdownPDFWhitespace(b) || offsetText.Len() == 0 {
			return fmt.Errorf("generated PDF startxref value is malformed")
		}
		break
	}
	for {
		b, readErr := reader.ReadByte()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if !markdownPDFWhitespace(b) {
			return fmt.Errorf("unexpected data between startxref and EOF")
		}
	}
	xrefOffset, err := strconv.ParseInt(offsetText.String(), 10, 64)
	if err != nil || xrefOffset < 0 || xrefOffset >= size {
		return fmt.Errorf("generated PDF startxref offset is invalid")
	}

	probe := make([]byte, 64<<10)
	n, readErr := file.ReadAt(probe, xrefOffset)
	if readErr != nil && readErr != io.EOF {
		return fmt.Errorf("reading generated PDF xref target: %w", readErr)
	}
	probe = probe[:n]
	probe = bytes.TrimLeft(probe, "\x00\t\n\f\r ")
	if len(probe) >= 4 && bytes.Equal(probe[:4], []byte("xref")) && (len(probe) == 4 || markdownPDFWhitespace(probe[4])) {
		return nil
	}
	if !markdownPDFIndirectObjectHeader(probe) {
		return fmt.Errorf("generated PDF startxref does not point to an xref structure")
	}
	stream := bytes.Index(probe, []byte("stream"))
	if stream < 0 || !markdownPDFXRefStreamType.Match(probe[:stream]) {
		return fmt.Errorf("generated PDF startxref does not point to an xref stream")
	}
	return nil
}

func markdownPDFLastNonWhitespace(file *os.File, size int64) (int64, error) {
	const blockSize = 64 << 10
	for end := size; end > 0; {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		block := make([]byte, end-start)
		if _, err := file.ReadAt(block, start); err != nil && err != io.EOF {
			return 0, fmt.Errorf("reading generated PDF trailer: %w", err)
		}
		for i := len(block) - 1; i >= 0; i-- {
			if !markdownPDFWhitespace(block[i]) {
				return start + int64(i), nil
			}
		}
		end = start
	}
	return 0, fmt.Errorf("generated PDF is empty")
}

func markdownPDFLastSequence(file *os.File, end int64, sequence []byte) (int64, error) {
	const blockSize = 64 << 10
	for end > 0 {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		block := make([]byte, end-start)
		if _, err := file.ReadAt(block, start); err != nil && err != io.EOF {
			return 0, fmt.Errorf("reading generated PDF trailer: %w", err)
		}
		if index := bytes.LastIndex(block, sequence); index >= 0 {
			return start + int64(index), nil
		}
		if start == 0 {
			break
		}
		end = start + int64(len(sequence)-1)
	}
	return 0, fmt.Errorf("terminal marker was not found")
}

func markdownPDFWhitespace(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func markdownPDFIndirectObjectHeader(data []byte) bool {
	i := 0
	skip := func() {
		for i < len(data) && markdownPDFWhitespace(data[i]) {
			i++
		}
	}
	integer := func() bool {
		start := i
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			i++
		}
		return i > start
	}
	skip()
	if !integer() {
		return false
	}
	if i >= len(data) || !markdownPDFWhitespace(data[i]) {
		return false
	}
	skip()
	if !integer() {
		return false
	}
	if i >= len(data) || !markdownPDFWhitespace(data[i]) {
		return false
	}
	skip()
	if !bytes.HasPrefix(data[i:], []byte("obj")) {
		return false
	}
	i += len("obj")
	return i == len(data) || markdownPDFWhitespace(data[i])
}

// completeMarkdownPDFOutput owns the output until streaming, close, strict PDF
// validation and the final browser-policy boundary all succeed. Every failure
// leaves no publishable path and removes the partial or invalid file.
func completeMarkdownPDFOutput(
	ctx context.Context,
	file *os.File,
	print func(io.Writer) error,
	policyBoundary func() error,
) (output string, err error) {
	if file == nil {
		return "", fmt.Errorf("missing Markdown PDF output operation")
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if print == nil || policyBoundary == nil {
		err = fmt.Errorf("missing Markdown PDF output operation")
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = print(file); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = validateMarkdownPDFFile(file.Name()); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = policyBoundary(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return file.Name(), nil
}
