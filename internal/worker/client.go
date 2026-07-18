package worker

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var workerURL = os.Getenv("PDFNEST_WORKER_URL")

var Client = &http.Client{
	Timeout: 30 * time.Minute,
}

func GetWorkerURL() string {
	if workerURL == "" {
		workerURL = "http://0.0.0.0:8000"
	}
	return workerURL
}

func CreateMultipartRequest(
	inputPath string,
	build func(*multipart.Writer) error,
) (*bytes.Buffer, string, error) {

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, "", err
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			return
		}
	}(file)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile(
		"file",
		filepath.Base(inputPath),
	)
	if err != nil {
		return nil, "", err
	}

	if _, err := io.Copy(part, file); err != nil {
		return nil, "", err
	}

	if build != nil {
		if err := build(writer); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return body, writer.FormDataContentType(), nil
}
