package ocrv2

import (
	"context"
	"strings"
	"time"
)

const ProfileOCRTextV2 = "OCR_TEXT_V2"

type RoutingPolicy string

const (
	RoutingAuto             RoutingPolicy = "AUTO"
	RoutingFast             RoutingPolicy = "FAST"
	RoutingQuality          RoutingPolicy = "QUALITY"
	RoutingGeometry         RoutingPolicy = "GEOMETRY"
	RoutingLanguageFallback RoutingPolicy = "LANGUAGE_FALLBACK"
)

type TextRequest struct {
	RequestID     string
	Profile       string
	Language      string
	RoutingPolicy RoutingPolicy
}

type PageResult struct {
	PageIndex      int            `json:"page_index"`
	PageID         string         `json:"page_id"`
	Status         string         `json:"status"`
	Text           string         `json:"text"`
	Classification string         `json:"classification"`
	Source         string         `json:"source"`
	Language       map[string]any `json:"language"`
	WarningCodes   []string       `json:"warning_codes,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type TextResponse struct {
	SchemaVersion string       `json:"schema_version"`
	RequestID     string       `json:"request_id"`
	Profile       string       `json:"profile"`
	Status        string       `json:"status"`
	Text          string       `json:"text"`
	Pages         []PageResult `json:"pages"`
	Warnings      []string     `json:"warnings,omitempty"`
	Error         *Error       `json:"error,omitempty"`
}

type WorkerInvoker interface {
	Execute(ctx context.Context, inputPath string, request TextRequest) (*TextResponse, error)
}

type LanguageCapability struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type RoutingCapability struct {
	ID          RoutingPolicy `json:"id"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Available   bool          `json:"available"`
}

type Capabilities struct {
	Languages              []LanguageCapability `json:"languages"`
	RoutingModes           []RoutingCapability  `json:"routing_modes"`
	QualityEngineAvailable bool                 `json:"quality_engine_available"`
}

type CapabilitiesInvoker interface {
	GetCapabilities(ctx context.Context) (*Capabilities, error)
}

type JobSubmitRequest struct {
	RequestID     string
	Profile       string
	Language      string
	RoutingPolicy RoutingPolicy
	SourceKey     string
	SourceName    string
	OwnerIdentity string
	TotalPages    int
}

type JobStatus struct {
	JobID          string            `json:"job_id"`
	Status         string            `json:"status"`
	Profile        string            `json:"profile"`
	Language       string            `json:"language"`
	RoutingPolicy  RoutingPolicy     `json:"routing_policy"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	Progress       int               `json:"progress"`
	TotalPages     int               `json:"total_pages"`
	CompletedPages int               `json:"completed_pages"`
	FailedPages    []int             `json:"failed_pages,omitempty"`
	CurrentPage    *int              `json:"current_page,omitempty"`
	PageStatuses   map[string]string `json:"page_statuses,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
	ResultKey      string            `json:"result_key,omitempty"`
	OwnerIdentity  string            `json:"owner_identity"`
	ErrorCode      string            `json:"error_code,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func (j JobStatus) ResultAvailable() bool {
	return strings.TrimSpace(j.ResultKey) != ""
}

type AsyncJobInvoker interface {
	SubmitJob(ctx context.Context, request JobSubmitRequest) (*JobStatus, error)
	GetJob(ctx context.Context, jobID string) (*JobStatus, error)
	GetResult(ctx context.Context, jobID string) (*TextResponse, error)
	CancelJob(ctx context.Context, jobID, ownerIdentity string) (*JobStatus, error)
}

type JobProgress struct {
	CompletedPages int               `json:"completed_pages"`
	TotalPages     int               `json:"total_pages"`
	FailedPages    []int             `json:"failed_pages,omitempty"`
	CurrentPage    *int              `json:"current_page,omitempty"`
	PageStatuses   map[string]string `json:"page_statuses,omitempty"`
	Percent        int               `json:"percent"`
}

type PublicJobStatus struct {
	JobID           string        `json:"job_id"`
	Status          string        `json:"status"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	StartedAt       *time.Time    `json:"started_at,omitempty"`
	FinishedAt      *time.Time    `json:"finished_at,omitempty"`
	Profile         string        `json:"profile"`
	Language        string        `json:"language"`
	RoutingPolicy   RoutingPolicy `json:"routing_policy"`
	Progress        JobProgress   `json:"progress"`
	Warnings        []string      `json:"warnings,omitempty"`
	ResultAvailable bool          `json:"result_available"`
	Error           *Error        `json:"error,omitempty"`
}
