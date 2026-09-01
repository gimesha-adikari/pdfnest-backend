package ocrv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/uploads"

	"github.com/google/uuid"
)

type Service struct {
	worker       WorkerInvoker
	jobs         AsyncJobInvoker
	capabilities CapabilitiesInvoker
	maxPages     int
	maxBytes     int64
	artifacts    ArtifactStore
}

func (s *Service) CreateSearchablePDFJob(ctx context.Context, inputs []*uploads.File, request TextRequest, ownerIdentity string) (*JobStatus, error) {
	if s == nil || s.jobs == nil || s.artifacts == nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Searchable PDF V2 job service is not configured"}
	}
	searchableJobs, ok := s.jobs.(SearchableJobInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Searchable PDF V2 job service is not configured"}
	}
	if request.Profile != ProfileSearchablePDFV2 || strings.TrimSpace(ownerIdentity) == "" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if len(inputs) == 0 || len(inputs) > s.maxPages {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	sources := make([]SourceFile, 0, len(inputs))
	keys := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if err := validateImageInput(input); err != nil {
			return nil, &RequestError{Code: ErrInvalidInput}
		}
		ext := filepath.Ext(input.Header.Filename)
		key := storage.BuildKey("jobs/ocr_v2/searchable_pdf/input", ext)
		if err := s.artifacts.UploadFile(input.Path, key, input.Header.Header.Get("Content-Type")); err != nil {
			for _, uploaded := range keys {
				_ = s.artifacts.DeleteObject(context.Background(), uploaded)
			}
			return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Searchable PDF input storage is unavailable"}
		}
		keys = append(keys, key)
		sources = append(sources, SourceFile{SourceKey: key, SourceName: filepath.Base(input.Header.Filename), ContentType: input.Header.Header.Get("Content-Type")})
	}
	job, err := searchableJobs.SubmitJob(ctx, JobSubmitRequest{RequestID: request.RequestID, Profile: ProfileSearchablePDFV2, Language: request.Language, LanguageMode: request.LanguageMode, Languages: request.Languages, LanguageUsage: LanguageUsageRanking(ownerIdentity), RoutingPolicy: request.RoutingPolicy, SourceFiles: sources, SourceName: filepath.Base(inputs[0].Header.Filename), OwnerIdentity: ownerIdentity, TotalPages: len(sources)})
	if err != nil {
		for _, key := range keys {
			_ = s.artifacts.DeleteObject(context.Background(), key)
		}
		return nil, err
	}
	return job, nil
}

func (s *Service) CreateStructuredJob(ctx context.Context, input *uploads.File, request TextRequest, ownerIdentity string) (*JobStatus, error) {
	if s == nil || s.jobs == nil || s.artifacts == nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Structured OCR job service is not configured"}
	}
	structuredJobs, ok := s.jobs.(StructuredJobInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Structured OCR job service is not configured"}
	}
	if (request.Profile != ProfileDocumentExtractionV2 && request.Profile != ProfilePDFMarkdownV2) || strings.TrimSpace(ownerIdentity) == "" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if err := validatePDFInput(input); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if int64(input.Header.Size) > s.maxBytes {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	key := storage.BuildKey("jobs/ocr_v2/structured/input", ".pdf")
	if err := s.artifacts.UploadFile(input.Path, key, "application/pdf"); err != nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Structured OCR input storage is unavailable"}
	}
	pageCount, err := uploads.CheckPDFPageLimit(input.Path, "OCR_V2_MAX_PAGES", s.maxPages)
	if err != nil {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	job, err := structuredJobs.SubmitJob(ctx, JobSubmitRequest{RequestID: request.RequestID, Profile: request.Profile, Language: request.Language, LanguageMode: request.LanguageMode, Languages: request.Languages, LanguageUsage: LanguageUsageRanking(ownerIdentity), RoutingPolicy: request.RoutingPolicy, SourceKey: key, SourceName: filepath.Base(input.Header.Filename), OwnerIdentity: ownerIdentity, TotalPages: pageCount})
	if err != nil {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, err
	}
	return job, nil
}

func (s *Service) CreateMarkupJob(ctx context.Context, input *uploads.File, request TextRequest, markup MarkupRequest, ownerIdentity string) (*JobStatus, error) {
	if s == nil || s.jobs == nil || s.artifacts == nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "OCR-aware markup service is not configured"}
	}
	if strings.TrimSpace(ownerIdentity) == "" || request.Profile != ProfileMarkupV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if err := validatePDFInput(input); err != nil || strings.TrimSpace(markup.Query) == "" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if markup.Action != "highlight" && markup.Action != "underline" && markup.Action != "strikeout" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if markup.Mode != "smart" && markup.Mode != "ocr" && markup.Mode != "native" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if markup.Color == "" {
		markup.Color = "#FFFF00"
	}
	if len(markup.Color) != 7 || markup.Color[0] != '#' {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if int64(input.Header.Size) > s.maxBytes {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	key := storage.BuildKey("jobs/ocr_v2/markup/input", ".pdf")
	if err := s.artifacts.UploadFile(input.Path, key, "application/pdf"); err != nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Markup input storage is unavailable"}
	}
	pageCount, err := uploads.CheckPDFPageLimit(input.Path, "OCR_V2_MAX_PAGES", s.maxPages)
	if err != nil {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	asyncJobs, ok := s.jobs.(AsyncJobInvoker)
	if !ok {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Markup job service is not configured"}
	}
	job, err := asyncJobs.SubmitJob(ctx, JobSubmitRequest{
		RequestID: request.RequestID, Profile: ProfileMarkupV2, Language: request.Language, LanguageMode: request.LanguageMode, Languages: request.Languages, LanguageUsage: LanguageUsageRanking(ownerIdentity),
		RoutingPolicy: request.RoutingPolicy, SourceKey: key, SourceName: filepath.Base(input.Header.Filename),
		OwnerIdentity: ownerIdentity, TotalPages: pageCount, Markup: &markup,
	})
	if err != nil {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, err
	}
	return job, nil
}

func validatePDFInput(input *uploads.File) error {
	if input == nil || input.Header == nil || input.Path == "" || input.Header.Size <= 0 {
		return fmt.Errorf("empty PDF input")
	}
	if !strings.HasSuffix(strings.ToLower(input.Header.Filename), ".pdf") {
		return fmt.Errorf("unsupported PDF input")
	}
	return uploads.ValidatePDFHeader(input.Path)
}

func validateImageInput(input *uploads.File) error {
	if input == nil || input.Header == nil || input.Path == "" || input.Header.Size <= 0 {
		return fmt.Errorf("empty image input")
	}
	name := strings.ToLower(input.Header.Filename)
	contentType := strings.ToLower(strings.TrimSpace(input.Header.Header.Get("Content-Type")))
	allowed := (strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp"))
	if !allowed || (contentType != "" && contentType != "application/octet-stream" && !strings.HasPrefix(contentType, "image/")) {
		return fmt.Errorf("unsupported image input")
	}
	file, err := os.Open(input.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(file, header); err != nil {
		return err
	}
	if !bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}) && !bytes.Equal(header[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) && !(string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP") {
		return fmt.Errorf("image signature is invalid")
	}
	return nil
}

func (s *Service) GetOwnedSearchableArtifact(ctx context.Context, jobID, ownerIdentity string) (*ArtifactResult, error) {
	searchableJobs, ok := s.jobs.(SearchableJobInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Searchable PDF V2 job service is not configured"}
	}
	job, err := s.GetOwnedJob(ctx, jobID, ownerIdentity)
	if err != nil {
		return nil, err
	}
	if job.Profile != ProfileSearchablePDFV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if !job.ResultAvailable() {
		return nil, &WorkerError{Code: ErrResultNotReady, Message: "Searchable PDF V2 result is not ready"}
	}
	return searchableJobs.GetArtifact(ctx, jobID)
}

func (s *Service) GetOwnedStructuredResult(ctx context.Context, jobID, ownerIdentity string) (json.RawMessage, error) {
	structuredJobs, ok := s.jobs.(StructuredJobInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Structured OCR job service is not configured"}
	}
	job, err := s.GetOwnedJob(ctx, jobID, ownerIdentity)
	if err != nil {
		return nil, err
	}
	if job.Profile != ProfileDocumentExtractionV2 && job.Profile != ProfilePDFMarkdownV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if !job.ResultAvailable() {
		return nil, &WorkerError{Code: ErrResultNotReady, Message: "Structured OCR result is not ready"}
	}
	return structuredJobs.GetStructuredResult(ctx, jobID)
}

func (s *Service) GetOwnedMarkupArtifact(ctx context.Context, jobID, ownerIdentity string) (*ArtifactResult, error) {
	job, err := s.GetOwnedJob(ctx, jobID, ownerIdentity)
	if err != nil {
		return nil, err
	}
	if job.Profile != ProfileMarkupV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if !job.ResultAvailable() {
		return nil, &WorkerError{Code: ErrResultNotReady, Message: "Markup result is not ready"}
	}
	searchableJobs, ok := s.jobs.(SearchableJobInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "Markup result service is not configured"}
	}
	return searchableJobs.GetArtifact(ctx, jobID)
}

type ArtifactStore interface {
	UploadFile(path, key, contentType string) error
	DeleteObject(ctx context.Context, key string) error
}

type objectArtifactStore struct{}

func (objectArtifactStore) UploadFile(path, key, contentType string) error {
	if !storage.RemoteStorageEnabled() {
		return storage.SaveLocalFile(context.Background(), key, path)
	}
	store, err := storage.Default()
	if err != nil {
		return err
	}
	return store.UploadFile(path, key, contentType)
}

func (objectArtifactStore) DeleteObject(ctx context.Context, key string) error {
	return storage.DeleteObject(ctx, key)
}

func NewService(workerClient WorkerInvoker) *Service {
	maxPages := uploads.GetEnvInt("OCR_V2_MAX_PAGES", uploads.GetEnvInt("MAX_PAGES_OCR", 150))
	maxBytes := int64(uploads.GetEnvInt("OCR_V2_STRUCTURED_MAX_BYTES", uploads.GetEnvInt("OCR_V2_MAX_BYTES", 100*1024*1024)))
	service := &Service{worker: workerClient, maxPages: maxPages, maxBytes: maxBytes, artifacts: objectArtifactStore{}}
	if jobClient, ok := workerClient.(AsyncJobInvoker); ok {
		service.jobs = jobClient
	}
	if capabilityClient, ok := workerClient.(CapabilitiesInvoker); ok {
		service.capabilities = capabilityClient
	}
	return service
}

func (s *Service) GetCapabilities(ctx context.Context) (*Capabilities, error) {
	if s == nil || s.capabilities == nil {
		return nil, &WorkerError{Code: ErrEngineUnavailable, Message: "OCR V2 capabilities are not configured"}
	}
	return s.capabilities.GetCapabilities(ctx)
}

func (s *Service) ExecuteText(ctx context.Context, inputPath string, request TextRequest) (*TextResponse, error) {
	if s == nil || s.worker == nil {
		return nil, &WorkerError{Code: ErrEngineUnavailable, Message: "OCR V2 worker is not configured"}
	}
	if strings.TrimSpace(request.Profile) != ProfileOCRTextV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if _, err := os.Stat(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if err := uploads.ValidatePDFHeader(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if _, err := uploads.CheckPDFPageLimit(inputPath, "OCR_V2_MAX_PAGES", s.maxPages); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	return s.worker.Execute(ctx, inputPath, request)
}

func (s *Service) PreviewMarkup(ctx context.Context, inputPath string, request TextRequest) (*MarkupPreviewResponse, error) {
	if s == nil || s.worker == nil {
		return nil, &WorkerError{Code: ErrEngineUnavailable, Message: "OCR markup preview is not configured"}
	}
	previewer, ok := s.worker.(MarkupPreviewInvoker)
	if !ok {
		return nil, &WorkerError{Code: ErrEngineUnavailable, Message: "OCR markup preview is not configured"}
	}
	if strings.TrimSpace(request.Profile) != ProfileMarkupV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if _, err := os.Stat(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if err := uploads.ValidatePDFHeader(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if _, err := uploads.CheckPDFPageLimit(inputPath, "OCR_V2_MAX_PAGES", s.maxPages); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	return previewer.Preview(ctx, inputPath, request)
}

func (s *Service) CreateJob(ctx context.Context, inputPath string, request TextRequest, ownerIdentity string) (*JobStatus, error) {
	if s == nil || s.jobs == nil || s.artifacts == nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "OCR V2 job service is not configured"}
	}
	if strings.TrimSpace(request.Profile) != ProfileOCRTextV2 {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(ownerIdentity) == "" {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if strings.TrimSpace(request.Language) == "" {
		return nil, &RequestError{Code: ErrUnsupportedLanguage}
	}
	if _, err := os.Stat(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	if err := uploads.ValidatePDFHeader(inputPath); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	pageCount, err := uploads.CheckPDFPageLimit(inputPath, "OCR_V2_MAX_PAGES", s.maxPages)
	if err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}

	name := filepath.Base(inputPath)
	key := storage.BuildKey("jobs/ocr_v2/input", ".pdf")
	if err := s.artifacts.UploadFile(inputPath, key, "application/pdf"); err != nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "OCR V2 input storage is unavailable"}
	}
	job, err := s.jobs.SubmitJob(ctx, JobSubmitRequest{
		RequestID:     request.RequestID,
		Profile:       request.Profile,
		Language:      request.Language,
		LanguageMode:  request.LanguageMode,
		Languages:     request.Languages,
		LanguageUsage: LanguageUsageRanking(ownerIdentity),
		RoutingPolicy: request.RoutingPolicy,
		SourceKey:     key,
		SourceName:    name,
		OwnerIdentity: ownerIdentity,
		TotalPages:    pageCount,
	})
	if err != nil {
		_ = s.artifacts.DeleteObject(context.Background(), key)
		return nil, err
	}
	return job, nil
}

func (s *Service) GetOwnedJob(ctx context.Context, jobID, ownerIdentity string) (*JobStatus, error) {
	if s == nil || s.jobs == nil {
		return nil, &WorkerError{Code: ErrTaskStorageUnavailable, Message: "OCR V2 job service is not configured"}
	}
	if _, err := uuid.Parse(strings.TrimSpace(jobID)); err != nil {
		return nil, &RequestError{Code: ErrInvalidInput}
	}
	job, err := s.jobs.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil || job.JobID == "" {
		return nil, &WorkerError{Code: ErrNotFound, Message: "OCR V2 job was not found"}
	}
	if job.OwnerIdentity != ownerIdentity {
		return nil, &WorkerError{Code: "FORBIDDEN", Message: "OCR V2 job ownership mismatch"}
	}
	return job, nil
}

func (s *Service) GetOwnedResult(ctx context.Context, jobID, ownerIdentity string) (*TextResponse, error) {
	job, err := s.GetOwnedJob(ctx, jobID, ownerIdentity)
	if err != nil {
		return nil, err
	}
	if !job.ResultAvailable() {
		return nil, &WorkerError{Code: ErrResultNotReady, Message: "OCR V2 result is not ready"}
	}
	return s.jobs.GetResult(ctx, jobID)
}

func (s *Service) CancelJob(ctx context.Context, jobID, ownerIdentity string) (*JobStatus, error) {
	if _, err := s.GetOwnedJob(ctx, jobID, ownerIdentity); err != nil {
		return nil, err
	}
	return s.jobs.CancelJob(ctx, jobID, ownerIdentity)
}
