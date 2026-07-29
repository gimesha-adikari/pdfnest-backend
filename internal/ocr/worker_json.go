package ocr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"pdfnest-backend/internal/worker"
)

func postJSONToWorker(route string, payload any) (*http.Response, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, fmt.Errorf("failed to encode worker json payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, worker.GetWorkerURL()+route, &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}

	return resp, nil
}
