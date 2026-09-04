package ocrv2

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const ProfileOCRTextV2 = "OCR_TEXT_V2"
const ProfileSearchablePDFV2 = "SEARCHABLE_PDF_V2"
const ProfileDocumentExtractionV2 = "DOCUMENT_EXTRACTION_V2"
const ProfilePDFMarkdownV2 = "PDF_MARKDOWN_V2"
const ProfileMarkupV2 = "MARKUP_V2"

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
	LanguageMode  string
	Languages     []string
	LanguageUsage map[string]float64
	RoutingPolicy RoutingPolicy
	// PageIndex scopes the temporary markup preview to one zero-based source
	// page. It is intentionally not part of durable job requests.
	PageIndex *int
}

type MarkupRequest struct {
	Action string
	Mode   string
	Query  string
	Color  string
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

// MarkupPreviewWord is the minimal authorized projection needed to select
// OCR-backed text in the browser. It contains no engine or storage details.
type MarkupPreviewWord struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Width      float64  `json:"width"`
	Height     float64  `json:"height"`
	Order      int      `json:"order"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type MarkupPreviewPage struct {
	PageIndex         int                 `json:"page_index"`
	PageNumber        int                 `json:"page_number"`
	PageID            string              `json:"page_id"`
	Width             float64             `json:"width"`
	Height            float64             `json:"height"`
	Rotation          int                 `json:"rotation"`
	CoordinateSpace   string              `json:"coordinate_space"`
	CropBox           []float64           `json:"crop_box,omitempty"`
	Classification    string              `json:"classification"`
	Kind              string              `json:"kind"`
	SelectionMode     string              `json:"selection_mode"`
	Status            string              `json:"status"`
	HasSelectableText bool                `json:"has_selectable_text"`
	WordCount         int                 `json:"word_count"`
	ReadingOrder      []string            `json:"reading_order"`
	Words             []MarkupPreviewWord `json:"words"`
	Language          map[string]any      `json:"language,omitempty"`
}

type MarkupPreviewResponse struct {
	SchemaVersion string              `json:"schema_version"`
	Profile       string              `json:"profile"`
	Status        string              `json:"status"`
	PageCount     int                 `json:"page_count"`
	Pages         []MarkupPreviewPage `json:"pages"`
}

type WorkerInvoker interface {
	Execute(ctx context.Context, inputPath string, request TextRequest) (*TextResponse, error)
}

type MarkupPreviewInvoker interface {
	Preview(ctx context.Context, inputPath string, request TextRequest) (*MarkupPreviewResponse, error)
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
	Languages              []LanguageCapability    `json:"languages"`
	LanguagePolicy         map[string]any          `json:"language_policy,omitempty"`
	RoutingModes           []RoutingCapability     `json:"routing_modes"`
	QualityEngineAvailable bool                    `json:"quality_engine_available"`
	SearchablePDF          SearchablePDFCapability `json:"searchable_pdf"`
}

type SearchablePDFCapability struct {
	Available            bool     `json:"available"`
	EngineID             string   `json:"engine_id"`
	RequiredCapabilities []string `json:"required_capabilities"`
	InputFormats         []string `json:"input_formats"`
}

type CapabilitiesInvoker interface {
	GetCapabilities(ctx context.Context) (*Capabilities, error)
}

type JobSubmitRequest struct {
	RequestID     string
	Profile       string
	Language      string
	LanguageMode  string
	Languages     []string
	LanguageUsage map[string]float64
	RoutingPolicy RoutingPolicy
	SourceKey     string
	SourceName    string
	SourceFiles   []SourceFile
	OwnerIdentity string
	TotalPages    int
	Markup        *MarkupRequest
}

type SourceFile struct {
	SourceKey   string `json:"source_key"`
	SourceName  string `json:"source_name"`
	ContentType string `json:"content_type"`
}

type ArtifactResult struct {
	Bytes       []byte
	Filename    string
	ContentType string
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

type SearchableJobInvoker interface {
	SubmitJob(ctx context.Context, request JobSubmitRequest) (*JobStatus, error)
	GetArtifact(ctx context.Context, jobID string) (*ArtifactResult, error)
}

type StructuredJobInvoker interface {
	SubmitJob(ctx context.Context, request JobSubmitRequest) (*JobStatus, error)
	GetJob(ctx context.Context, jobID string) (*JobStatus, error)
	GetStructuredResult(ctx context.Context, jobID string) (json.RawMessage, error)
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
