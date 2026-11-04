package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"pdfnest-backend/internal/worker"
)

const maxWorkerReadinessResponseBytes = 8 * 1024

func probeWorkerReadiness(ctx context.Context) (string, map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, worker.GetWorkerURL()+"/internal/health/ready", nil)
	if err != nil {
		return "", nil, errors.New("worker readiness request could not be created")
	}
	resp, err := worker.Client.Do(req)
	if err != nil {
		return "", nil, errors.New("worker readiness probe failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return "", nil, errors.New("worker readiness probe was not authenticated or unavailable")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkerReadinessResponseBytes+1))
	if err != nil || len(body) > maxWorkerReadinessResponseBytes {
		return "", nil, errors.New("worker readiness response is invalid")
	}
	var payload struct {
		Status       string          `json:"status"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, errors.New("worker readiness response is invalid")
	}
	if payload.Status != "ready" && payload.Status != "not_ready" {
		return "", nil, errors.New("worker readiness response is invalid")
	}
	return payload.Status, payload.Capabilities, nil
}
