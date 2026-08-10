package optimize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOptimizePDF_Basic(t *testing.T) {
	// Create a dummy input PDF file
	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "input.pdf")

	// Use a minimal valid PDF header
	pdfContent := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kinds [] /Count 0 >>\nendobj\nxref\n0 3\n0000000000 65535 f\n0000000009 00000 n\n0000000058 00000 n\ntrailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n109\n%%EOF\n")
	if err := os.WriteFile(inputPath, pdfContent, 0644); err != nil {
		t.Fatalf("failed to write test input pdf: %v", err)
	}

	service := NewService()
	outputPath, err := service.OptimizePDF(context.Background(), inputPath)
	if err != nil {
		t.Fatalf("OptimizePDF returned unexpected error: %v", err)
	}
	defer os.Remove(outputPath)

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file stat failed: %v", err)
	}

	if outputInfo.Size() == 0 {
		t.Errorf("expected non-empty output file, got 0 bytes")
	}

	// Zero Size Expansion Invariant check
	if outputInfo.Size() > int64(len(pdfContent)) {
		t.Errorf("Zero Expansion Violation: output size (%d) > input size (%d)", outputInfo.Size(), len(pdfContent))
	}
}
