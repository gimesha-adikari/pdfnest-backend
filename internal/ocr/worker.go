package ocr

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"pdfnest-backend/internal/worker"
)

func postSingleFileToWorker(
	ctx context.Context,
	inputPath string,
	fieldName string,
	route string,
	fields map[string]string,
) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open input file: %w", err)
	}

	// Stream multipart body through an io.Pipe to avoid buffering the
	// entire file in Go heap memory before sending to the worker.
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		defer file.Close()

		part, err := writer.CreateFormFile(fieldName, filepath.Base(inputPath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, file); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		for k, v := range fields {
			if err := writer.WriteField(k, v); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}

		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.GetWorkerURL()+route, pr)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}

	return resp, nil
}

func postMultipleFilesToWorker(
	ctx context.Context,
	inputPaths []string,
	fieldName string,
	route string,
	fields map[string]string,
) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Stream multipart body through an io.Pipe to avoid buffering all
	// input files in Go heap memory before sending to the worker.
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		for _, inputPath := range inputPaths {
			file, err := os.Open(inputPath)
			if err != nil {
				_ = pw.CloseWithError(fmt.Errorf("failed to open input file %q: %w", inputPath, err))
				return
			}

			part, err := writer.CreateFormFile(fieldName, filepath.Base(inputPath))
			if err != nil {
				_ = file.Close()
				_ = pw.CloseWithError(fmt.Errorf("failed to create multipart file field: %w", err))
				return
			}

			if _, err := io.Copy(part, file); err != nil {
				_ = file.Close()
				_ = pw.CloseWithError(fmt.Errorf("failed to copy file into multipart body: %w", err))
				return
			}

			_ = file.Close()
		}

		for k, v := range fields {
			if err := writer.WriteField(k, v); err != nil {
				_ = pw.CloseWithError(fmt.Errorf("failed to write multipart field %q: %w", k, err))
				return
			}
		}

		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(fmt.Errorf("failed to finalize multipart body: %w", err))
			return
		}
		_ = pw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.GetWorkerURL()+route, pr)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}

	return resp, nil
}
