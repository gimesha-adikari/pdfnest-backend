package edit

import "encoding/json"

type JobSubmissionResponse struct {
	Success   bool   `json:"success"`
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	QueueName string `json:"queue_name"`
}

type JobRecord struct {
	ID              string         `json:"id"`
	JobType         string         `json:"job_type"`
	Status          string         `json:"status"`
	Progress        int            `json:"progress"`
	Message         string         `json:"message"`
	Result          map[string]any `json:"result"`
	Error           string         `json:"error"`
	CancelRequested bool           `json:"cancel_requested"`
}

type CompileRequest struct {
	SourceTracker  string          `json:"source_tracker"`
	UprightTracker string          `json:"upright_tracker,omitempty"`
	Pages          json.RawMessage `json:"pages,omitempty"`
}

type ExtractResponse struct {
	Success        bool            `json:"success"`
	Pages          json.RawMessage `json:"pages,omitempty"`
	SourceTracker  string          `json:"source_tracker,omitempty"`
	UprightTracker string          `json:"upright_tracker,omitempty"`
	Error          string          `json:"error,omitempty"`
}
