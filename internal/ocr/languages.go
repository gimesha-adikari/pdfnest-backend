package ocr

import (
	"encoding/json"
	"fmt"
	"net/http"

	"pdfnest-backend/internal/worker"
)

type OCRLanguage struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type OCRLanguagesResponse struct {
	Default   string        `json:"default"`
	Languages []OCRLanguage `json:"languages"`
}

func GetOCRLanguages() (*OCRLanguagesResponse, error) {
	req, err := http.NewRequest(
		http.MethodGet,
		worker.GetWorkerURL()+"/api/v1/ocr/languages",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("worker returned %s", resp.Status)
	}

	var out OCRLanguagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed decoding OCR languages response: %w", err)
	}

	if out.Default == "" {
		out.Default = "eng"
	}

	return &out, nil
}
