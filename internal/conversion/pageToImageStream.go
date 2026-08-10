package conversion

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *ConversionService) ConvertPageToImageStream(ctx context.Context, fileHeader *multipart.FileHeader, pageNum int, scale float64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	src, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file payload stream: %w", err)
	}
	defer src.Close()

	tempDir := os.TempDir()

	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		ext = ".pdf"
	}

	tempFileName := fmt.Sprintf("pdfnest-preview-%s%s", uuid.New().String(), ext)
	tempFilePath := filepath.Join(tempDir, tempFileName)

	dst, err := os.Create(tempFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate disk memory space for temporary vector context: %w", err)
	}
	defer func() {
		_ = dst.Close()
	}()
	defer os.Remove(tempFilePath)

	if _, err = io.Copy(dst, src); err != nil {
		return nil, fmt.Errorf("disk write failure on payload compilation pass: %w", err)
	}
	if err := dst.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temporary payload file: %w", err)
	}

	targetPdfPath := tempFilePath

	if ext != ".pdf" {
		compiledPdfPath, err := s.OfficeToPdf(ctx, tempFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to compile office document for preview generation: %w", err)
		}
		targetPdfPath = compiledPdfPath
		defer os.Remove(targetPdfPath)
	}

	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	pdfFile, err := os.Open(targetPdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open target pdf for worker request: %w", err)
	}

	// Stream multipart body through an io.Pipe to avoid buffering the
	// entire PDF in Go heap memory for a single-page preview request.
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		defer pdfFile.Close()

		part, err := writer.CreateFormFile("file", filepath.Base(targetPdfPath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, pdfFile); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		_ = writer.WriteField("page", fmt.Sprintf("%d", pageNum))
		_ = writer.WriteField("dpi", fmt.Sprintf("%f", 72.0*scale))

		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(workerBaseURL, "/")+"/api/v1/render/page", pr)
	if err != nil {
		return nil, fmt.Errorf("failed to build worker request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{
		Timeout: 5 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("page render failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("page render failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(errBody)))
	}

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read rendered image bytes: %w", err)
	}

	if _, err = jpeg.Decode(bytes.NewReader(imgBytes)); err != nil {
		return nil, fmt.Errorf("rendered image is not a valid jpeg: %w", err)
	}

	return imgBytes, nil
}
