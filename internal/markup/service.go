package markup

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type service struct{}

func NewService() Service {
	return &service{}
}

func encodeBoxes(boxes any) (string, error) {
	b, err := json.Marshal(boxes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *service) HighlightPDF(inputPath string, boxes []HighlightBox, mode, filePassword string) (*workerJobSubmission, error) {
	boxesJSON, err := encodeBoxes(boxes)
	if err != nil {
		return nil, fmt.Errorf("failed to encode highlight boxes: %w", err)
	}

	extra := map[string]string{
		"boxes": boxesJSON,
		"mode":  mode,
	}
	if filePassword != "" {
		extra["file_password"] = filePassword
	}

	return postMultipartFile(workerBaseURL()+"/api/v1/markup/highlight", "file", inputPath, extra)
}

func (s *service) UnderlinePDF(inputPath string, boxes []UnderlineBox, mode, filePassword string) (*workerJobSubmission, error) {
	boxesJSON, err := encodeBoxes(boxes)
	if err != nil {
		return nil, fmt.Errorf("failed to encode underline boxes: %w", err)
	}

	extra := map[string]string{
		"boxes": boxesJSON,
		"mode":  mode,
	}
	if filePassword != "" {
		extra["file_password"] = filePassword
	}

	return postMultipartFile(workerBaseURL()+"/api/v1/markup/underline", "file", inputPath, extra)
}

func (s *service) StrikeoutPDF(inputPath string, boxes []StrikeoutBox, mode, filePassword string) (*workerJobSubmission, error) {
	boxesJSON, err := encodeBoxes(boxes)
	if err != nil {
		return nil, fmt.Errorf("failed to encode strikeout boxes: %w", err)
	}

	extra := map[string]string{
		"boxes": boxesJSON,
		"mode":  mode,
	}
	if filePassword != "" {
		extra["file_password"] = filePassword
	}

	return postMultipartFile(workerBaseURL()+"/api/v1/markup/strikeout", "file", inputPath, extra)
}

func (s *service) GetJobStatus(jobID string) (*workerJobRecord, error) {
	return getJSON[workerJobRecord](workerBaseURL()+"/api/v1/jobs/"+jobID, 30*time.Second)
}

func (s *service) GetJobDownload(jobID string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, workerBaseURL()+"/api/v1/markup/jobs/"+jobID+"/download", nil)
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
