package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"pdfnest-backend/internal/worker"
)

type WorkerJobSubmission struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	QueueName string `json:"queue_name"`
}

type WorkerJobRecord struct {
	ID              string         `json:"id"`
	JobType         string         `json:"job_type"`
	Status          string         `json:"status"`
	Progress        int            `json:"progress"`
	Message         string         `json:"message"`
	Result          map[string]any `json:"result"`
	Error           string         `json:"error"`
	ErrorCode       string         `json:"error_code"`
	CancelRequested bool           `json:"cancel_requested"`
	Payload         map[string]any `json:"payload"`
}

type editorExtractRequest struct {
	SourceKey    string   `json:"source_key"`
	FilePassword string   `json:"file_password,omitempty"`
	SourceName   string   `json:"source_name,omitempty"`
	OCRV2        bool     `json:"ocr_v2,omitempty"`
	Consumer     string   `json:"consumer,omitempty"`
	LanguageMode string   `json:"language_mode,omitempty"`
	Languages    []string `json:"languages,omitempty"`
}

func normalizedEditorLanguage(language EditorLanguageRequest) EditorLanguageRequest {
	if language.Mode == "" {
		language.Mode = "EXPLICIT"
	}
	if len(language.Languages) == 0 {
		language.Languages = []string{"eng"}
	}
	return language
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

func postJSON(url string, payload any) (*WorkerJobSubmission, error) {
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

	var submission WorkerJobSubmission
	if err := json.NewDecoder(resp.Body).Decode(&submission); err != nil {
		return nil, fmt.Errorf("failed to decode job submission response: %w", err)
	}

	if submission.JobID == "" {
		return nil, fmt.Errorf("worker returned empty job id")
	}

	return &submission, nil
}

func (s *service) ExtractLayout(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/editor/extract", editorExtractRequest{
		SourceKey:    sourceKey,
		FilePassword: filePassword,
		SourceName:   sourceName,
	})
}

func (s *service) ExtractLayoutForLegacyEditor(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/editor/extract", editorExtractRequest{
		SourceKey:    sourceKey,
		FilePassword: filePassword,
		SourceName:   sourceName,
		Consumer:     "legacy_editor",
	})
}

func (s *service) ExtractLayoutV2(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error) {
	return s.ExtractLayoutV2WithLanguage(sourceKey, filePassword, sourceName, EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"eng"}})
}

func (s *service) ExtractLayoutV2WithLanguage(sourceKey string, filePassword string, sourceName string, language EditorLanguageRequest) (*WorkerJobSubmission, error) {
	language = normalizedEditorLanguage(language)
	return postJSON(workerBaseURL()+"/api/v1/editor/extract", editorExtractRequest{SourceKey: sourceKey, FilePassword: filePassword, SourceName: sourceName, OCRV2: true, Consumer: "studio", LanguageMode: language.Mode, Languages: language.Languages})
}

func (s *service) ExtractLayoutV2ForGeneralEditor(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error) {
	return s.ExtractLayoutV2ForGeneralEditorWithLanguage(sourceKey, filePassword, sourceName, EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"eng"}})
}

func (s *service) ExtractLayoutV2ForGeneralEditorWithLanguage(sourceKey string, filePassword string, sourceName string, language EditorLanguageRequest) (*WorkerJobSubmission, error) {
	language = normalizedEditorLanguage(language)
	return postJSON(workerBaseURL()+"/api/v1/editor/extract", editorExtractRequest{SourceKey: sourceKey, FilePassword: filePassword, SourceName: sourceName, OCRV2: true, Consumer: "general_editor", LanguageMode: language.Mode, Languages: language.Languages})
}

func (s *service) CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*WorkerJobSubmission, error) {
	return postJSON(workerBaseURL()+"/api/v1/editor/compile", editorCompileRequest{
		SourceKey:    sourceKey,
		PagesJSONKey: pagesJSONKey,
		SourceName:   sourceName,
	})
}

func (s *service) GetJobStatus(jobID string) (*WorkerJobRecord, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create job status request: %w", err)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch job status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("job status failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}

	var job WorkerJobRecord
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job status response: %w", err)
	}

	return &job, nil
}

func (s *service) CancelJob(jobID string) (*WorkerJobRecord, error) {
	req, err := http.NewRequest(http.MethodPost, workerBaseURL()+"/api/v1/jobs/"+jobID+"/cancel", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build cancellation request: %w", err)
	}
	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel job: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("job cancellation failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(b)))
	}
	var job WorkerJobRecord
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode cancellation response: %w", err)
	}
	return &job, nil
}

func (s *service) GetJobDownload(jobID string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/editor/jobs/"+jobID+"/download", nil)
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
