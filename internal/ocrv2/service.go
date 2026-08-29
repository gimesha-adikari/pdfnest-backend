package ocrv2

import (
	"context"
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
	artifacts    ArtifactStore
}

type ArtifactStore interface {
	UploadFile(path, key, contentType string) error
	DeleteObject(ctx context.Context, key string) error
}

type objectArtifactStore struct{}

func (objectArtifactStore) UploadFile(path, key, contentType string) error {
	if strings.TrimSpace(os.Getenv("R2_BUCKET")) == "" {
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
	service := &Service{worker: workerClient, maxPages: maxPages, artifacts: objectArtifactStore{}}
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
	if strings.TrimSpace(request.Language) == "" || strings.EqualFold(strings.TrimSpace(request.Language), "auto") || strings.EqualFold(strings.TrimSpace(request.Language), "detect") {
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
	if strings.TrimSpace(request.Language) == "" || strings.EqualFold(strings.TrimSpace(request.Language), "auto") || strings.EqualFold(strings.TrimSpace(request.Language), "detect") {
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
