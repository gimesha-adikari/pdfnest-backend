package conversion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/uploads"
	"strings"

	"github.com/google/uuid"
)

func (s *ConversionService) OfficeToPdf(ctx context.Context, inputPath string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	finalPdfPath := filepath.Join(tempDir, "office-compiled-"+sessionID+".pdf")

	format, err := officeFormatFromPath(inputPath)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ProcessOfficeToPDF(ctx, format, inputPath, finalPdfPath); err != nil {
		_ = os.Remove(finalPdfPath)
		return "", err
	}
	if err := uploads.ValidatePDFHeader(finalPdfPath); err != nil {
		_ = os.Remove(finalPdfPath)
		return "", fmt.Errorf("worker returned invalid PDF artifact: %w", err)
	}

	return finalPdfPath, nil
}

func officeFormatFromPath(inputPath string) (string, error) {
	switch strings.ToLower(filepath.Ext(inputPath)) {
	case ".doc", ".docx":
		return "docx", nil
	case ".xls", ".xlsx":
		return "xlsx", nil
	case ".ppt", ".pptx":
		return "pptx", nil
	default:
		return "", fmt.Errorf("unsupported office document extension %q", filepath.Ext(inputPath))
	}
}
