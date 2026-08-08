package uploads

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

var (
	ErrInvalidPDFHeader     = errors.New("invalid PDF file header: signature %PDF- not found")
	ErrEmptyFile            = errors.New("uploaded file is empty")
	ErrPageLimitExceeded    = errors.New("document page count exceeds maximum allowed limit")
	ErrCannotDeterminePages = errors.New("cannot determine PDF page count")
)

// GetEnvInt reads an environment variable as an integer, returning defaultValue if unset/invalid.
func GetEnvInt(key string, defaultValue int) int {
	valStr := strings.TrimSpace(os.Getenv(key))
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return defaultValue
	}
	return val
}

// GetPDFPageCount returns the exact page count of a PDF file using pdfcpu.
// It fails closed if the page count cannot be safely determined.
func GetPDFPageCount(path string) (int, error) {
	if err := ValidatePDFHeader(path); err != nil {
		return 0, err
	}
	pageCount, err := api.PageCountFile(path)
	if err != nil || pageCount <= 0 {
		return 0, fmt.Errorf("%w: %v", ErrCannotDeterminePages, err)
	}
	return pageCount, nil
}

// CheckPDFPageLimit verifies that the PDF at path does not exceed the limit configured by envKey or defaultLimit.
// Returns the page count on success, or an error if over limit or unreadable.
func CheckPDFPageLimit(path string, envKey string, defaultLimit int) (int, error) {
	pageCount, err := GetPDFPageCount(path)
	if err != nil {
		return 0, err
	}
	limit := GetEnvInt(envKey, defaultLimit)
	if pageCount > limit {
		return pageCount, fmt.Errorf("%w: document has %d pages, maximum allowed for this operation is %d", ErrPageLimitExceeded, pageCount, limit)
	}
	return pageCount, nil
}

// ValidatePDFHeader reads the initial 5 bytes of a file and verifies the %PDF- magic signature.
func ValidatePDFHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file for PDF validation: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 5)
	n, err := io.ReadFull(f, buf)
	if err != nil {
		if n == 0 {
			return ErrEmptyFile
		}
		return ErrInvalidPDFHeader
	}

	if !bytes.HasPrefix(buf, []byte("%PDF-")) {
		return ErrInvalidPDFHeader
	}

	return nil
}

// ValidatePDF checks whether this uploaded file has a valid %PDF- magic signature.
func (f *File) ValidatePDF() error {
	if f == nil || f.Path == "" {
		return ErrEmptyFile
	}
	return ValidatePDFHeader(f.Path)
}

// MustPDFFile extracts a single file from the upload context and validates its %PDF- signature.
func MustPDFFile(c *fiber.Ctx, field string) (*File, error) {
	file, err := MustFile(c, field)
	if err != nil {
		return nil, err
	}
	if err := file.ValidatePDF(); err != nil {
		return nil, err
	}
	return file, nil
}

// MustPDFFiles extracts files from the upload context and validates their %PDF- signatures.
func MustPDFFiles(c *fiber.Ctx, field string) ([]*File, error) {
	files, err := MustFiles(c, field)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := f.ValidatePDF(); err != nil {
			return nil, fmt.Errorf("file %q is invalid: %w", f.Header.Filename, err)
		}
	}
	return files, nil
}
