package markup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"pdfnest-backend/internal/worker"
	"strings"
	"time"
)

func workerBaseURL() string {
	return worker.GetWorkerURL()
}

type markupRequest struct {
	SourceKey  string `json:"source_key"`
	PayloadKey string `json:"payload_key"`
	SourceName string `json:"source_name,omitempty"`
}

func postJSON(url string, payload any) (*workerJobSubmission, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request json: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("worker request failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}

	var submission workerJobSubmission
	if err := json.NewDecoder(resp.Body).Decode(&submission); err != nil {
		return nil, fmt.Errorf("failed to decode job submission response: %w", err)
	}

	if submission.JobID == "" {
		return nil, fmt.Errorf("worker returned empty job id")
	}

	return &submission, nil
}

func getJSON[T any](url string, timeout time.Duration) (*T, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

func (s *service) HighlightPDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/markup/highlight", markupRequest{
		SourceKey:  sourceKey,
		PayloadKey: payloadKey,
		SourceName: sourceName,
	})
}

func (s *service) UnderlinePDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/markup/underline", markupRequest{
		SourceKey:  sourceKey,
		PayloadKey: payloadKey,
		SourceName: sourceName,
	})
}

func (s *service) StrikeoutPDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/markup/strikeout", markupRequest{
		SourceKey:  sourceKey,
		PayloadKey: payloadKey,
		SourceName: sourceName,
	})
}

func (s *service) GetJobStatus(jobID string) (*workerJobRecord, error) {
	return getJSON[workerJobRecord](workerBaseURL()+"/api/v1/jobs/"+jobID, 30*time.Second)
}

func (s *service) GetJobDownload(jobID string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/markup/jobs/"+jobID+"/download", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build download request: %w", err)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}

	return resp, nil
}
