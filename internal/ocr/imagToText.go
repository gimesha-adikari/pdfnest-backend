package ocr

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

func (s *ocrService) ImageToTextPDF(ctx context.Context, imagePaths []string, lang string) (string, error) {
	if len(imagePaths) == 0 {
		return "", fmt.Errorf("no images provided")
	}

	tempDir := os.TempDir()
	outputPDFPath := filepath.Join(tempDir, "ocr-searchable-"+uuid.New().String()+".pdf")

	if lang == "" {
		lang = "eng"
	}

	resp, err := postMultipleFilesToWorker(
		ctx,
		imagePaths,
		"images",
		"/api/v1/ocr/to-text-pdf",
		map[string]string{
			"lang": lang,
		},
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ocr worker returned %s: %s", resp.Status, string(errBody))
	}

	out, err := os.Create(outputPDFPath)
	if err != nil {
		return "", fmt.Errorf("failed creating output pdf file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed saving OCR output: %w", err)
	}

	return outputPDFPath, nil
}
