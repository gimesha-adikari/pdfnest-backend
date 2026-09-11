package studio

import (
	"time"

	"github.com/google/uuid"

	"pdfnest-backend/internal/studio/models"
)

// StudioJobDTO is the deliberately small public representation of a Studio
// job. The persistence model contains request parameters, staged storage keys,
// and worker result data that are needed only by the backend. Keeping the
// browser contract explicit prevents a large editor layout from being echoed
// back on every submit/status response.
type StudioJobDTO struct {
	ID              uuid.UUID  `json:"id"`
	DocumentID      uuid.UUID  `json:"document_id"`
	SessionID       uuid.UUID  `json:"session_id"`
	BaseVersionID   uuid.UUID  `json:"base_version_id"`
	ResultVersionID *uuid.UUID `json:"result_version_id,omitempty"`
	EditorStateID   *uuid.UUID `json:"editor_state_id,omitempty"`
	JobType         string     `json:"job_type"`
	Status          string     `json:"status"`
	Progress        int        `json:"progress"`
	Message         string     `json:"message"`
	Error           string     `json:"error,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ReconciledAt    *time.Time `json:"reconciled_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type StudioJobResponse struct {
	Job                *StudioJobDTO `json:"job"`
	IsIdempotentReplay bool          `json:"is_idempotent_replay"`
}

func publicStudioJob(job *models.StudioJob) *StudioJobDTO {
	if job == nil {
		return nil
	}
	return &StudioJobDTO{
		ID:              job.ID,
		DocumentID:      job.DocumentID,
		SessionID:       job.SessionID,
		BaseVersionID:   job.BaseVersionID,
		ResultVersionID: job.ResultVersionID,
		EditorStateID:   job.EditorStateID,
		JobType:         job.JobType,
		Status:          job.Status,
		Progress:        job.Progress,
		Message:         job.Message,
		Error:           job.Error,
		ErrorCode:       job.ErrorCode,
		ReconciledAt:    job.ReconciledAt,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
	}
}

func publicStudioJobResult(result *StudioJobResult) *StudioJobResponse {
	if result == nil {
		return nil
	}
	return &StudioJobResponse{
		Job:                publicStudioJob(result.Job),
		IsIdempotentReplay: result.IsIdempotentReplay,
	}
}
