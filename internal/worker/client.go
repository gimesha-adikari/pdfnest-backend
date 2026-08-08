package worker

import (
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var Client = &http.Client{
	Timeout: 30 * time.Minute,
}

func GetWorkerURL() string {
	workerURL := os.Getenv("PDFNEST_WORKER_URL")
	if strings.TrimSpace(workerURL) == "" {
		return "http://localhost:8000"
	}
	return strings.TrimRight(workerURL, "/")
}

func CreateMultipartRequest(
	inputPath string,
	build func(*multipart.Writer) error,
) (io.Reader, string, error) {
	file, err := os.Open(inputPath)
	if err != nil {
		return nil, "", err
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		defer file.Close()

		part, err := writer.CreateFormFile(
			"file",
			filepath.Base(inputPath),
		)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, file); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if build != nil {
			if err := build(writer); err != nil {
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

	return pr, contentType, nil
}
