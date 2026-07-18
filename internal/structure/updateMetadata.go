package structure

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/worker"

	"github.com/google/uuid"
)

type metadataResponse struct {
	Title    string `json:"title"`
	Author   string `json:"author"`
	Subject  string `json:"subject"`
	Keywords string `json:"keywords"`
}

func (s *structureService) UpdateMetadataPDF(
	inputPath string,
	metadata map[string]string,
	password string,
) (string, error) {

	tempDir := os.TempDir()
	outputPath := filepath.Join(
		tempDir,
		"metadata-"+uuid.New().String()+".pdf",
	)

	file, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	body, contentType, err := worker.CreateMultipartRequest(
		inputPath,
		func(writer *multipart.Writer) error {
			err := writer.WriteField("title", metadata["Title"])
			if err != nil {
				return err
			}
			err = writer.WriteField("author", metadata["Author"])
			if err != nil {
				return err
			}
			err = writer.WriteField("subject", metadata["Subject"])
			if err != nil {
				return err
			}
			err = writer.WriteField("keywords", metadata["Keywords"])
			if err != nil {
				return err
			}

			if password != "" {
				err := writer.WriteField("file_password", password)
				if err != nil {
					return err
				}
			}

			return nil
		},
	)

	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		worker.GetWorkerURL()+"/api/v1/metadata/write",
		body,
	)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata worker unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("metadata worker failed: %s", string(b))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}

	return outputPath, nil
}

func (s *structureService) GetMetadataPDF(
	inputPath string,
	password string,
) (map[string]string, error) {

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body, contentType, err := worker.CreateMultipartRequest(
		inputPath,
		func(writer *multipart.Writer) error {
			if password != "" {
				err := writer.WriteField("file_password", password)
				if err != nil {
					return err
				}
			}
			return nil
		},
	)

	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		worker.GetWorkerURL()+"/api/v1/metadata/read",
		body,
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata worker unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("metadata worker failed: %s", string(b))
	}

	var parsed metadataResponse

	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("invalid metadata response: %w", err)
	}

	return map[string]string{
		"title":    parsed.Title,
		"author":   parsed.Author,
		"subject":  parsed.Subject,
		"keywords": parsed.Keywords,
	}, nil
}
