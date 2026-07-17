package conversion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ProcessOfficeConversion(format, inputPath, outputPath string) error {
	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	inputFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input file: %w", err)
	}
	defer inputFile.Close()

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer func() {
			_ = writer.Close()
			_ = pw.Close()
		}()

		if err := writer.WriteField("format", strings.ToLower(format)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		part, err := writer.CreateFormFile("file", filepath.Base(inputPath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, inputFile); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	convertURL := strings.TrimRight(workerBaseURL, "/") + "/api/v1/office/convert"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, convertURL, pr)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{
		Timeout: 30 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("worker request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBuf bytes.Buffer
		_, _ = io.Copy(&errBuf, resp.Body)
		return fmt.Errorf("worker conversion failed: status=%s body=%s", resp.Status, strings.TrimSpace(errBuf.String()))
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return fmt.Errorf("failed to write converted file: %w", err)
	}

	return nil
}
