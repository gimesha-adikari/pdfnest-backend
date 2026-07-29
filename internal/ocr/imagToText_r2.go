package ocr

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (s *ocrService) ImageToTextPDFFromR2(files []R2ImageRef, lang string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("no images provided")
	}

	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "eng"
	}

	tempDir := os.TempDir()
	outputPDFPath := filepath.Join(tempDir, "ocr-searchable-"+uuid.New().String()+".pdf")

	resp, err := postJSONToWorker("/api/v1/ocr/to-text-pdf-r2", map[string]any{
		"lang":  lang,
		"files": files,
	})
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
