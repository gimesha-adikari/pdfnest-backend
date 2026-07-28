package ocr

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"pdfnest-backend/internal/worker"
)

func postSingleFileToWorker(
	inputPath string,
	fieldName string,
	route string,
	fields map[string]string,
) (*http.Response, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open input file: %w", err)
	}
	defer file.Close()

	part, err := writer.CreateFormFile(fieldName, filepath.Base(inputPath))
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart file field: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("failed to copy file into multipart body: %w", err)
	}

	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("failed to write multipart field %q: %w", k, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, worker.GetWorkerURL()+route, &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}

	return resp, nil
}

func postMultipleFilesToWorker(
	inputPaths []string,
	fieldName string,
	route string,
	fields map[string]string,
) (*http.Response, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for _, inputPath := range inputPaths {
		file, err := os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open input file %q: %w", inputPath, err)
		}

		part, err := writer.CreateFormFile(fieldName, filepath.Base(inputPath))
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("failed to create multipart file field: %w", err)
		}

		if _, err := io.Copy(part, file); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("failed to copy file into multipart body: %w", err)
		}

		_ = file.Close()
	}

	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("failed to write multipart field %q: %w", k, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, worker.GetWorkerURL()+route, &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}

	return resp, nil
}
