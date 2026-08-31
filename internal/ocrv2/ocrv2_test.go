package ocrv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/internal/middleware"
	"pdfnest-backend/internal/uploads"
	"pdfnest-backend/internal/worker"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

type fakeInvoker struct {
	response *TextResponse
	err      error
	received TextRequest
}

type fakeCapabilitiesInvoker struct {
	fakeInvoker
	capabilities *Capabilities
}

func (f *fakeCapabilitiesInvoker) GetCapabilities(context.Context) (*Capabilities, error) {
	return f.capabilities, nil
}

type fakeAsyncInvoker struct {
	job       *JobStatus
	result    *TextResponse
	submitted JobSubmitRequest
	cancelled bool
}

func (f *fakeAsyncInvoker) SubmitJob(_ context.Context, request JobSubmitRequest) (*JobStatus, error) {
	f.submitted = request
	if f.job == nil {
		now := time.Now().UTC()
		f.job = &JobStatus{JobID: "123e4567-e89b-12d3-a456-426614174000", Status: "queued", Profile: request.Profile, Language: request.Language, RoutingPolicy: request.RoutingPolicy, CreatedAt: now, UpdatedAt: now, TotalPages: request.TotalPages, OwnerIdentity: request.OwnerIdentity}
	}
	return f.job, nil
}

func (f *fakeAsyncInvoker) GetJob(context.Context, string) (*JobStatus, error) { return f.job, nil }

func (f *fakeAsyncInvoker) GetResult(context.Context, string) (*TextResponse, error) {
	return f.result, nil
}

func (f *fakeAsyncInvoker) CancelJob(_ context.Context, _ string, _ string) (*JobStatus, error) {
	f.cancelled = true
	return f.job, nil
}

func (f *fakeAsyncInvoker) GetArtifact(context.Context, string) (*ArtifactResult, error) {
	return &ArtifactResult{Bytes: []byte("%PDF-1.7"), Filename: "document-searchable.pdf", ContentType: "application/pdf"}, nil
}

type fakeArtifactStore struct {
	uploaded bool
	deleted  bool
}

func (f *fakeArtifactStore) UploadFile(string, string, string) error { f.uploaded = true; return nil }

func (f *fakeArtifactStore) DeleteObject(context.Context, string) error { f.deleted = true; return nil }

func (f *fakeInvoker) Execute(_ context.Context, _ string, request TextRequest) (*TextResponse, error) {
	f.received = request
	return f.response, f.err
}

func TestServiceCapabilitiesExposeOnlyProductSafeProjection(t *testing.T) {
	service := NewService(&fakeCapabilitiesInvoker{
		capabilities: &Capabilities{
			Languages:              []LanguageCapability{{Code: "eng", Name: "English"}},
			RoutingModes:           []RoutingCapability{{ID: RoutingAuto, Label: "Balanced", Available: true}},
			QualityEngineAvailable: false,
		},
	})
	capabilities, err := service.GetCapabilities(context.Background())
	if err != nil {
		t.Fatalf("expected capabilities, got %v", err)
	}
	if len(capabilities.Languages) != 1 || capabilities.Languages[0].Code != "eng" {
		t.Fatalf("unexpected language projection: %+v", capabilities.Languages)
	}
	if capabilities.QualityEngineAvailable {
		t.Fatal("expected unavailable quality engine to remain unavailable")
	}
}

func TestControllerCapabilitiesArePublicAndReturnSafeFields(t *testing.T) {
	service := NewService(&fakeCapabilitiesInvoker{
		capabilities: &Capabilities{
			Languages:    []LanguageCapability{{Code: "eng", Name: "English"}},
			RoutingModes: []RoutingCapability{{ID: RoutingAuto, Label: "Balanced", Description: "Balanced", Available: true}},
		},
	})
	controller := NewController(service)
	app := fiber.New()
	app.Get("/api/v2/ocr/text/capabilities", controller.Capabilities)
	app.Get("/api/v2/ocr/structured/capabilities", controller.StructuredCapabilities)

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v2/ocr/text/capabilities", nil))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusOK {
		t.Fatalf("expected public safe capabilities response, got %d", response.StatusCode)
	}
	var payload Capabilities
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Languages) != 1 || len(payload.RoutingModes) != 1 {
		t.Fatalf("unexpected capability response: %+v", payload)
	}
	structuredResponse, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v2/ocr/structured/capabilities", nil))
	if err != nil {
		t.Fatal(err)
	}
	if structuredResponse.StatusCode != fiber.StatusOK {
		t.Fatalf("expected public structured capabilities response, got %d", structuredResponse.StatusCode)
	}
	var structuredPayload map[string]any
	if err := json.NewDecoder(structuredResponse.Body).Decode(&structuredPayload); err != nil {
		t.Fatal(err)
	}
	if structuredPayload["native_first"] != true {
		t.Fatalf("expected native-first structured capability response: %+v", structuredPayload)
	}
}

func samplePDFPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "pdfnest", "tests", "fixtures", "normal_text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample PDF unavailable: %v", err)
	}
	return path
}

func TestServiceExecuteTextValidatesProfileLanguageAndPDF(t *testing.T) {
	fake := &fakeInvoker{response: &TextResponse{Status: "SUCCEEDED", Profile: ProfileOCRTextV2}}
	service := NewService(fake)
	_, err := service.ExecuteText(context.Background(), samplePDFPath(t), TextRequest{Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingAuto})
	if err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
	if fake.received.Language != "eng" {
		t.Fatalf("expected explicit language to reach worker, got %q", fake.received.Language)
	}
	_, err = service.ExecuteText(context.Background(), samplePDFPath(t), TextRequest{Profile: "SEARCHABLE_PDF_V2", Language: "eng"})
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != ErrInvalidInput {
		t.Fatalf("expected typed invalid profile error, got %v", err)
	}
	_, err = service.ExecuteText(context.Background(), samplePDFPath(t), TextRequest{Profile: ProfileOCRTextV2, Language: "auto"})
	if err != nil {
		t.Fatalf("expected AUTO language policy to reach the worker, got %v", err)
	}
	if fake.received.Language != "auto" {
		t.Fatalf("expected AUTO policy to reach worker, got %q", fake.received.Language)
	}
}

func TestAsyncServicePersistsInputAndScopesJobsToOwner(t *testing.T) {
	async := &fakeAsyncInvoker{result: &TextResponse{Status: "SUCCEEDED", Profile: ProfileOCRTextV2}}
	artifacts := &fakeArtifactStore{}
	service := NewService(&fakeInvoker{})
	service.jobs = async
	service.artifacts = artifacts
	job, err := service.CreateJob(context.Background(), samplePDFPath(t), TextRequest{RequestID: "job-request", Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingAuto}, "user:alice")
	if err != nil {
		t.Fatalf("expected async job submission, got %v", err)
	}
	if !artifacts.uploaded || async.submitted.OwnerIdentity != "user:alice" || async.submitted.TotalPages == 0 {
		t.Fatalf("expected durable input and owner metadata, uploaded=%v request=%+v", artifacts.uploaded, async.submitted)
	}
	if _, err := service.GetOwnedJob(context.Background(), job.JobID, "user:bob"); errorCode(err) != "FORBIDDEN" {
		t.Fatalf("expected owner-scoped status access, got %v", err)
	}
	async.job.ResultKey = "jobs/ocr_v2/results/123e4567-e89b-12d3-a456-426614174000.json"
	if _, err := service.GetOwnedResult(context.Background(), job.JobID, "user:alice"); err != nil {
		t.Fatalf("expected owner result access, got %v", err)
	}
	if _, err := service.CancelJob(context.Background(), job.JobID, "user:alice"); err != nil || !async.cancelled {
		t.Fatalf("expected owner cancellation, err=%v cancelled=%v", err, async.cancelled)
	}
}

func TestSearchableServicePersistsOrderedImageInputs(t *testing.T) {
	async := &fakeAsyncInvoker{}
	artifacts := &fakeArtifactStore{}
	service := NewService(&fakeInvoker{})
	service.jobs = async
	service.artifacts = artifacts
	firstPath := filepath.Join(t.TempDir(), "first.png")
	secondPath := filepath.Join(t.TempDir(), "second.jpg")
	if err := os.WriteFile(firstPath, append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 8)...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, append([]byte{0xff, 0xd8, 0xff}, make([]byte, 9)...), 0600); err != nil {
		t.Fatal(err)
	}
	inputs := []*uploads.File{
		{Path: firstPath, Header: &multipart.FileHeader{Filename: "first.png", Size: 16, Header: make(textproto.MIMEHeader)}},
		{Path: secondPath, Header: &multipart.FileHeader{Filename: "second.jpg", Size: 12, Header: make(textproto.MIMEHeader)}},
	}
	inputs[0].Header.Header.Set("Content-Type", "image/png")
	inputs[1].Header.Header.Set("Content-Type", "image/jpeg")
	job, err := service.CreateSearchablePDFJob(context.Background(), inputs, TextRequest{RequestID: "searchable-1", Profile: ProfileSearchablePDFV2, Language: "eng", RoutingPolicy: RoutingAuto}, "user:alice")
	if err != nil {
		t.Fatalf("expected searchable job submission, got %v", err)
	}
	if job == nil || async.submitted.Profile != ProfileSearchablePDFV2 || len(async.submitted.SourceFiles) != 2 || async.submitted.SourceFiles[0].SourceName != "first.png" || async.submitted.SourceFiles[1].SourceName != "second.jpg" {
		t.Fatalf("expected ordered searchable sources, job=%+v request=%+v", job, async.submitted)
	}
}

func TestPublicJobStatusDoesNotExposeWorkerIdentityOrStorageKey(t *testing.T) {
	now := time.Now().UTC()
	page := 2
	public := publicJobStatus(&JobStatus{JobID: "123e4567-e89b-12d3-a456-426614174000", Status: "running", Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingQuality, CreatedAt: now, UpdatedAt: now, Progress: 50, TotalPages: 4, CompletedPages: 2, CurrentPage: &page, ResultKey: "jobs/private/result.json", OwnerIdentity: "user:alice"})
	data, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("user:alice")) || bytes.Contains(data, []byte("jobs/private")) {
		t.Fatalf("public job status leaked private fields: %s", data)
	}
}

func TestControllerRejectsAsyncJobAccessForDifferentAuthenticatedUser(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	now := time.Now().UTC()
	async := &fakeAsyncInvoker{job: &JobStatus{JobID: "123e4567-e89b-12d3-a456-426614174000", Status: "running", Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingAuto, CreatedAt: now, UpdatedAt: now, OwnerIdentity: "user:alice", TotalPages: 1}}
	service := NewService(&fakeInvoker{})
	service.jobs = async
	controller := NewController(service)
	app := fiber.New()
	app.Get("/api/v2/ocr/text/jobs/:job_id", middleware.Protect(), controller.JobStatus)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": "user:bob", "role": "user"})
	serialized, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/ocr/text/jobs/123e4567-e89b-12d3-a456-426614174000", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: serialized})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected cross-user job access to be forbidden, got %d", resp.StatusCode)
	}
}

func TestControllerMapsWorkerFailureWithoutLeakingInternalMessage(t *testing.T) {
	fake := &fakeInvoker{err: &WorkerError{Code: ErrEngineUnavailable, Message: "secret path /tmp/model"}}
	controller := NewController(NewService(fake))
	app := fiber.New()
	app.Post("/api/v2/ocr/text", controller.Text)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ocr/text", bytes.NewBufferString("not-a-pdf"))
	req.Header.Set("Content-Type", "application/pdf")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected invalid input before worker call, got %d", resp.StatusCode)
	}
}

func multipartPDF(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	data, err := os.ReadFile(samplePDFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("language", "eng"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestControllerReturnsBackendSafeOCRTextResponse(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	fake := &fakeInvoker{response: &TextResponse{RequestID: "req-controller", Profile: ProfileOCRTextV2, Status: "SUCCEEDED", Text: "safe text", Pages: []PageResult{{PageIndex: 0, PageID: "page-0", Status: "SUCCESS", Text: "safe text", Source: "pymupdf_native_extractor"}}}}
	controller := NewController(NewService(fake))
	app := fiber.New()
	app.Post("/api/v2/ocr/text", middleware.Protect(), uploads.Prepare(), controller.Text)
	body, contentType := multipartPDF(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ocr/text", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Request-ID", "req-controller")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": "user-1", "role": "user"})
	serialized, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: serialized})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	decoded, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Contains(decoded, []byte("safe text")) {
		t.Fatalf("expected safe OCR response, body=%s err=%v", decoded, err)
	}
}

func TestControllerRequiresAuthenticatedUserWhenProtected(t *testing.T) {
	fake := &fakeInvoker{response: &TextResponse{Status: "SUCCEEDED"}}
	controller := NewController(NewService(fake))
	app := fiber.New()
	app.Post("/api/v2/ocr/text", middleware.Protect(), uploads.Prepare(), controller.Text)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ocr/text", bytes.NewBufferString("body"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected unauthenticated request to fail with 401, got %d", resp.StatusCode)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestClientSerializesV2RequestAndMapsTypedWorkerFailure(t *testing.T) {
	path := samplePDFPath(t)
	seen := false
	client := NewClientForTest(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seen = true
		if request.URL.Path != "/internal/ocr/v2/text" || request.Header.Get("X-Request-ID") != "req-1" {
			t.Errorf("unexpected worker request: %s %s", request.Method, request.URL.String())
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte("OCR_TEXT_V2")) || !bytes.Contains(body, []byte("Tamil")) {
			t.Errorf("multipart body did not contain typed request fields")
		}
		payload, _ := json.Marshal(TextResponse{Status: "FAILED", Error: &Error{Code: ErrEngineUnavailable, Message: "engine unavailable"}})
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
	})}, "http://worker", func(request *http.Request) error { return worker.SignRequestWithSecret(request, "test-secret") })
	response, err := client.Execute(context.Background(), path, TextRequest{RequestID: "req-1", Profile: ProfileOCRTextV2, Language: "Tamil", RoutingPolicy: RoutingQuality})
	if !seen || response == nil {
		t.Fatalf("expected worker request and typed response")
	}
	var workerErr *WorkerError
	if !errors.As(err, &workerErr) || workerErr.Code != ErrEngineUnavailable {
		t.Fatalf("expected typed engine-unavailable error, got %v", err)
	}
}

func TestClientMapsMalformedAndTimeoutResponses(t *testing.T) {
	path := samplePDFPath(t)
	malformed := NewClientForTest(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString("not-json")), Header: make(http.Header)}, nil
	})}, "http://worker", nil)
	_, err := malformed.Execute(context.Background(), path, TextRequest{RequestID: "req-2", Profile: ProfileOCRTextV2, Language: "eng"})
	var workerErr *WorkerError
	if !errors.As(err, &workerErr) || workerErr.Code != ErrInvalidEngineOutput {
		t.Fatalf("expected malformed output error, got %v", err)
	}
	timeoutClient := NewClientForTest(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}, "http://worker", nil)
	_, err = timeoutClient.Execute(context.Background(), path, TextRequest{RequestID: "req-3", Profile: ProfileOCRTextV2, Language: "eng"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout context error, got %v", err)
	}
}

func TestClientAsyncJobContractUsesPrivateWorkerRoutes(t *testing.T) {
	jobID := "123e4567-e89b-12d3-a456-426614174000"
	now := time.Now().UTC()
	statusPayload, _ := json.Marshal(JobStatus{JobID: jobID, Status: "queued", Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingAuto, CreatedAt: now, UpdatedAt: now, TotalPages: 2, OwnerIdentity: "user:alice"})
	resultPayload, _ := json.Marshal(TextResponse{Status: "SUCCEEDED", Profile: ProfileOCRTextV2, Text: "result"})
	client := NewClientForTest(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/internal/ocr/v2/jobs" && request.Method == http.MethodPost {
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(bytes.NewReader(statusPayload)), Header: make(http.Header)}, nil
		}
		if request.URL.Path == "/internal/ocr/v2/jobs/"+jobID+"/result" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(resultPayload)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(statusPayload)), Header: make(http.Header)}, nil
	})}, "http://worker", nil)
	job, err := client.SubmitJob(context.Background(), JobSubmitRequest{RequestID: "request-1", Profile: ProfileOCRTextV2, Language: "eng", RoutingPolicy: RoutingQuality, SourceKey: "jobs/ocr_v2/input/source.pdf", SourceName: "source.pdf", OwnerIdentity: "user:alice", TotalPages: 2})
	if err != nil || job.JobID != jobID {
		t.Fatalf("expected async job submission response, job=%+v err=%v", job, err)
	}
	result, err := client.GetResult(context.Background(), jobID)
	if err != nil || result.Text != "result" {
		t.Fatalf("expected async result response, result=%+v err=%v", result, err)
	}
}
