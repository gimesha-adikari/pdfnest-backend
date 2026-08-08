package uploads

import (
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
)

func createTempFile(t *testing.T, prefix string, content []byte) string {
	t.Helper()
	tempFile, err := os.CreateTemp("", prefix+"-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer tempFile.Close()

	if len(content) > 0 {
		if _, err := tempFile.Write(content); err != nil {
			t.Fatalf("failed to write to temp file: %v", err)
		}
	}
	return tempFile.Name()
}

func TestValidatePDFHeader_ValidPDF(t *testing.T) {
	// Standard valid PDF header
	validHeader := []byte("%PDF-1.7\n%hello world test pdf content")
	path := createTempFile(t, "valid-pdf", validHeader)
	defer os.Remove(path)

	if err := ValidatePDFHeader(path); err != nil {
		t.Errorf("expected valid PDF header, got error: %v", err)
	}

	file := &File{
		Field:  "file",
		Header: &multipart.FileHeader{Filename: filepath.Base(path)},
		Path:   path,
	}
	if err := file.ValidatePDF(); err != nil {
		t.Errorf("expected File.ValidatePDF() to pass, got: %v", err)
	}
}

func TestValidatePDFHeader_FakePDFContent(t *testing.T) {
	// File named .pdf but containing plain text / image data
	fakeContent := []byte("This is plain text pretending to be a PDF file.")
	path := createTempFile(t, "fake-pdf", fakeContent)
	defer os.Remove(path)

	err := ValidatePDFHeader(path)
	if err == nil {
		t.Errorf("expected error for non-PDF content, got nil")
	}
	if err != ErrInvalidPDFHeader {
		t.Errorf("expected ErrInvalidPDFHeader, got: %v", err)
	}
}

func TestValidatePDFHeader_EmptyFile(t *testing.T) {
	path := createTempFile(t, "empty-file", []byte{})
	defer os.Remove(path)

	err := ValidatePDFHeader(path)
	if err == nil {
		t.Errorf("expected error for empty file, got nil")
	}
	if err != ErrEmptyFile {
		t.Errorf("expected ErrEmptyFile, got: %v", err)
	}
}

func TestValidatePDFHeader_TruncatedFile(t *testing.T) {
	// Only 3 bytes instead of 5
	path := createTempFile(t, "short-file", []byte("%PD"))
	defer os.Remove(path)

	err := ValidatePDFHeader(path)
	if err == nil {
		t.Errorf("expected error for truncated header, got nil")
	}
	if err != ErrInvalidPDFHeader {
		t.Errorf("expected ErrInvalidPDFHeader, got: %v", err)
	}
}

func TestValidatePDFHeader_UnusualContentType(t *testing.T) {
	// Valid PDF header even if Content-Type header on request was octet-stream
	path := createTempFile(t, "octet-pdf", []byte("%PDF-2.0 header bytes"))
	defer os.Remove(path)

	file := &File{
		Field:  "document",
		Header: &multipart.FileHeader{Filename: "doc.bin", Header: map[string][]string{"Content-Type": {"application/octet-stream"}}},
		Path:   path,
	}
	if err := file.ValidatePDF(); err != nil {
		t.Errorf("expected valid PDF header check to pass regardless of Content-Type header, got: %v", err)
	}
}

func TestGetEnvInt(t *testing.T) {
	t.Setenv("TEST_PAGE_LIMIT", "250")
	if val := GetEnvInt("TEST_PAGE_LIMIT", 100); val != 250 {
		t.Errorf("expected 250 from env, got %d", val)
	}

	t.Setenv("TEST_PAGE_LIMIT_INVALID", "invalid")
	if val := GetEnvInt("TEST_PAGE_LIMIT_INVALID", 100); val != 100 {
		t.Errorf("expected default 100 for invalid env, got %d", val)
	}

	if val := GetEnvInt("TEST_UNSET_VAR", 150); val != 150 {
		t.Errorf("expected default 150 for unset env, got %d", val)
	}
}

func TestCheckPDFPageLimit_UnreadableOrFakePDF(t *testing.T) {
	fakeContent := []byte("This is a fake PDF that fails page count inspection.")
	path := createTempFile(t, "fake-limit-pdf", fakeContent)
	defer os.Remove(path)

	_, err := CheckPDFPageLimit(path, "TEST_LIMIT", 100)
	if err == nil {
		t.Errorf("expected error for fake PDF, got nil")
	}
}
