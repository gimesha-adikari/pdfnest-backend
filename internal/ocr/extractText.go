package ocr

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

func (s *ocrService) ExtractTextFromPDF(ctx context.Context, inputPath string, lang string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	outputTextPath := filepath.Join(tempDir, "extracted-text-"+sessionID+".txt")

	if lang == "" {
		lang = "eng"
	}

	resp, err := postSingleFileToWorker(
		ctx,
		inputPath,
		"file",
		"/api/v1/ocr/extract-text",
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

	out, err := os.Create(outputTextPath)
	if err != nil {
		return "", fmt.Errorf("failed creating output plain text file: %w", err)
	}

	success := false
	defer func() {
		_ = out.Close()
		if !success {
			_ = os.Remove(outputTextPath)
		}
	}()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed saving OCR output: %w", err)
	}

	success = true
	return outputTextPath, nil
}
