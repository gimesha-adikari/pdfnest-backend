package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type workerJobSubmission struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	QueueName string `json:"queue_name"`
}

type workerJobRecord struct {
	ID              string         `json:"id"`
	JobType         string         `json:"job_type"`
	Status          string         `json:"status"`
	Progress        int            `json:"progress"`
	Message         string         `json:"message"`
	Result          map[string]any `json:"result"`
	Error           string         `json:"error"`
	CancelRequested bool           `json:"cancel_requested"`
}

type editorExtractRequest struct {
	SourceKey    string `json:"source_key"`
	FilePassword string `json:"file_password,omitempty"`
	SourceName   string `json:"source_name,omitempty"`
}

type editorCompileRequest struct {
	SourceKey    string `json:"source_key"`
	PagesJSONKey string `json:"pages_json_key"`
	SourceName   string `json:"source_name,omitempty"`
}

func workerBaseURL() string {
	base := os.Getenv("PDFNEST_WORKER_URL")
	if base == "" {
		base = "http://localhost:8000"
	}
	return strings.TrimRight(base, "/")
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

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
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

func (s *service) ExtractLayout(sourceKey string, filePassword string, sourceName string) (*workerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/editor/extract", editorExtractRequest{
		SourceKey:    sourceKey,
		FilePassword: filePassword,
		SourceName:   sourceName,
	})
}

func (s *service) CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*workerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/editor/compile", editorCompileRequest{
		SourceKey:    sourceKey,
		PagesJSONKey: pagesJSONKey,
		SourceName:   sourceName,
	})
}

func (s *service) GetJobStatus(jobID string) (*workerJobRecord, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create job status request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch job status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("job status failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}

	var job workerJobRecord
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job status response: %w", err)
	}

	return &job, nil
}

func (s *service) GetJobDownload(jobID string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/editor/jobs/"+jobID+"/download", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build download request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
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
