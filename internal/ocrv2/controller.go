package ocrv2

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

func (c *Controller) Text(cctx *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(cctx, "file")
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "A valid PDF upload is required."})
	}
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if len(requestID) > 128 {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Invalid request identifier."})
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingAuto)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported OCR V2 routing policy."})
	}
	request := textRequestFromContext(cctx, requestID, ProfileOCRTextV2, policy)
	ctx := cctx.UserContext()
	if ctx == nil {
		ctx = context.Background()
	}
	response, execErr := c.service.ExecuteText(ctx, upload.Path, request)
	if execErr == nil {
		return cctx.Status(fiber.StatusOK).JSON(response)
	}
	status := errorStatus(execErr)
	if response != nil {
		return cctx.Status(status).JSON(response)
	}
	return cctx.Status(status).JSON(Error{Code: errorCode(execErr), Message: publicMessage(execErr)})
}

func (c *Controller) Capabilities(cctx *fiber.Ctx) error {
	capabilities, err := c.service.GetCapabilities(cctx.UserContext())
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(capabilities)
}

func (c *Controller) CreateJob(cctx *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(cctx, "file")
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "A valid PDF upload is required."})
	}
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingAuto)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported OCR V2 routing policy."})
	}
	owner, _ := cctx.Locals("user_id").(string)
	request := textRequestFromContext(cctx, requestID, ProfileOCRTextV2, policy)
	job, execErr := c.service.CreateJob(cctx.UserContext(), upload.Path, request, owner)
	if execErr != nil {
		idempotency.Release(cctx, nil)
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: publicMessage(execErr)})
	}
	if err := idempotency.SetTaskID(cctx, job.JobID, nil); err != nil {
		_, _ = c.service.CancelJob(cctx.UserContext(), job.JobID, owner)
		return cctx.Status(fiber.StatusServiceUnavailable).JSON(Error{Code: ErrTaskStorageUnavailable, Message: "OCR V2 job persistence is temporarily unavailable."})
	}
	RecordLanguageSelection(owner, request.Language, false)
	return cctx.Status(fiber.StatusAccepted).JSON(publicJobStatus(job))
}

func (c *Controller) SearchableCapabilities(cctx *fiber.Ctx) error {
	capabilities, err := c.service.GetCapabilities(cctx.UserContext())
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(fiber.Map{
		"schema_version":  "ocr_v2_capabilities.v1",
		"profile":         ProfileSearchablePDFV2,
		"service_ready":   true,
		"languages":       capabilities.Languages,
		"language_policy": capabilities.LanguagePolicy,
		"routing_modes":   capabilities.RoutingModes,
		"searchable_pdf":  capabilities.SearchablePDF,
	})
}

func (c *Controller) StructuredCapabilities(cctx *fiber.Ctx) error {
	capabilities, err := c.service.GetCapabilities(cctx.UserContext())
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	structuredLanguages := make([]LanguageCapability, 0, len(capabilities.Languages))
	for _, language := range capabilities.Languages {
		switch strings.ToLower(strings.TrimSpace(language.Code)) {
		case "eng", "sin", "tam":
			structuredLanguages = append(structuredLanguages, language)
		}
	}
	return cctx.JSON(fiber.Map{
		"schema_version": "ocr_v2_structured_capabilities.v1",
		"service_ready":  true,
		"profiles": fiber.Map{
			ProfileDocumentExtractionV2: fiber.Map{"available": true, "input_formats": []string{"application/pdf"}},
			ProfilePDFMarkdownV2:        fiber.Map{"available": true, "input_formats": []string{"application/pdf"}},
		},
		"native_first":    true,
		"languages":       structuredLanguages,
		"language_policy": capabilities.LanguagePolicy,
		"routing_modes":   capabilities.RoutingModes,
		"engines": []fiber.Map{
			{"id": "pymupdf_native", "available": true, "capabilities": []string{"TEXT", "BLOCK_GEOMETRY", "READING_ORDER"}},
			{"id": "pdfplumber_native", "available": true, "capabilities": []string{"TABLES"}},
			{"id": "tesseract_v2", "available": true, "capabilities": []string{"TEXT", "WORD_GEOMETRY", "LINE_GEOMETRY", "BLOCK_GEOMETRY", "READING_ORDER"}},
		},
	})
}

func (c *Controller) MarkupCapabilities(cctx *fiber.Ctx) error {
	capabilities, err := c.service.GetCapabilities(cctx.UserContext())
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(fiber.Map{
		"schema_version":        "ocr_v2_markup_capabilities.v1",
		"service_ready":         true,
		"profile":               ProfileMarkupV2,
		"actions":               []string{"highlight", "underline", "strikeout"},
		"modes":                 []string{"smart", "ocr", "native"},
		"languages":             capabilities.Languages,
		"required_capabilities": []string{"TEXT", "WORD_GEOMETRY", "READING_ORDER"},
	})
}

func (c *Controller) MarkupPreview(cctx *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(cctx, "file")
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "A valid PDF upload is required."})
	}
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if len(requestID) > 128 {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Invalid request identifier."})
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingFast)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported OCR routing policy."})
	}
	request := textRequestFromContext(cctx, requestID, ProfileMarkupV2, policy)
	pageIndex, pageErr := optionalPreviewPageIndex(cctx)
	if pageErr != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Invalid preview page."})
	}
	request.PageIndex = pageIndex
	preview, execErr := c.service.PreviewMarkup(cctx.UserContext(), upload.Path, request)
	if execErr != nil {
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: previewPublicMessage(execErr)})
	}
	return cctx.Status(fiber.StatusOK).JSON(preview)
}

func optionalPreviewPageIndex(cctx *fiber.Ctx) (*int, error) {
	raw := strings.TrimSpace(cctx.FormValue("page_index"))
	if raw == "" {
		return nil, nil
	}
	pageIndex, err := strconv.Atoi(raw)
	if err != nil || pageIndex < 0 {
		return nil, errors.New("invalid preview page")
	}
	return &pageIndex, nil
}

func (c *Controller) CreateMarkupJob(cctx *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(cctx, "file")
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "A valid PDF upload is required."})
	}
	action := strings.TrimPrefix(cctx.Path(), "/api/v2/ocr/markup/")
	action = strings.TrimSuffix(action, "/jobs")
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingAuto)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported OCR V2 routing policy."})
	}
	owner, _ := cctx.Locals("user_id").(string)
	request := textRequestFromContext(cctx, requestID, ProfileMarkupV2, policy)
	markup := MarkupRequest{Action: action, Mode: strings.ToLower(strings.TrimSpace(cctx.FormValue("mode"))), Query: cctx.FormValue("query"), Color: strings.TrimSpace(cctx.FormValue("color"))}
	if markup.Mode == "" {
		markup.Mode = "smart"
	}
	job, execErr := c.service.CreateMarkupJob(cctx.UserContext(), upload, request, markup, owner)
	if execErr != nil {
		idempotency.Release(cctx, nil)
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: publicMessage(execErr)})
	}
	if err := idempotency.SetTaskID(cctx, job.JobID, nil); err != nil {
		_, _ = c.service.CancelJob(cctx.UserContext(), job.JobID, owner)
		return cctx.Status(fiber.StatusServiceUnavailable).JSON(Error{Code: ErrTaskStorageUnavailable, Message: "Markup job persistence is temporarily unavailable."})
	}
	RecordLanguageSelection(owner, request.Language, false)
	return cctx.Status(fiber.StatusAccepted).JSON(publicJobStatus(job))
}

func (c *Controller) ReplayMarkupJob(cctx *fiber.Ctx, record idempotency.Record) error {
	action := strings.TrimPrefix(cctx.Path(), "/api/v2/ocr/markup/")
	action = strings.TrimSuffix(action, "/jobs")
	return cctx.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": record.TaskID, "status": "QUEUED", "profile": ProfileMarkupV2, "action": action, "result_available": false, "idempotent_replay": true})
}

func (c *Controller) MarkupJobResult(cctx *fiber.Ctx) error {
	artifact, err := c.service.GetOwnedMarkupArtifact(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	cctx.Set("Content-Type", "application/pdf")
	cctx.Set("Content-Disposition", `attachment; filename="`+safeDownloadName(artifact.Filename)+`"`)
	return cctx.Send(artifact.Bytes)
}

func (c *Controller) CreateStructuredJob(cctx *fiber.Ctx) error {
	upload, err := uploads.MustFile(cctx, "file")
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "A valid PDF upload is required."})
	}
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingAuto)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported structured OCR routing policy."})
	}
	profile := strings.TrimSpace(cctx.FormValue("profile"))
	if profile == "" {
		if strings.Contains(cctx.Path(), "pdf-to-markdown-v2") {
			profile = ProfilePDFMarkdownV2
		} else {
			profile = ProfileDocumentExtractionV2
		}
	}
	if profile != ProfileDocumentExtractionV2 && profile != ProfilePDFMarkdownV2 {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported structured OCR profile."})
	}
	owner, _ := cctx.Locals("user_id").(string)
	request := textRequestFromContext(cctx, requestID, profile, policy)
	job, execErr := c.service.CreateStructuredJob(cctx.UserContext(), upload, request, owner)
	if execErr != nil {
		idempotency.Release(cctx, nil)
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: publicMessage(execErr)})
	}
	if err := idempotency.SetTaskID(cctx, job.JobID, nil); err != nil {
		_, _ = c.service.CancelJob(cctx.UserContext(), job.JobID, owner)
		return cctx.Status(fiber.StatusServiceUnavailable).JSON(Error{Code: ErrTaskStorageUnavailable, Message: "Structured OCR job persistence is temporarily unavailable."})
	}
	RecordLanguageSelection(owner, request.Language, false)
	return cctx.Status(fiber.StatusAccepted).JSON(publicJobStatus(job))
}

func (c *Controller) ReplayStructuredJob(cctx *fiber.Ctx, record idempotency.Record) error {
	profile := ProfileDocumentExtractionV2
	if strings.Contains(cctx.Path(), "pdf-to-markdown-v2") {
		profile = ProfilePDFMarkdownV2
	}
	return cctx.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": record.TaskID, "status": "QUEUED", "profile": profile, "result_available": false, "idempotent_replay": true})
}

func (c *Controller) StructuredJobResult(cctx *fiber.Ctx) error {
	result, err := c.service.GetOwnedStructuredResult(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	cctx.Type("json")
	return cctx.Send(result)
}

func (c *Controller) CreateSearchableJob(cctx *fiber.Ctx) error {
	inputs, err := searchableImageUploads(cctx)
	if err != nil {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "One or more supported image uploads are required."})
	}
	requestID := strings.TrimSpace(cctx.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	policy := RoutingPolicy(strings.ToUpper(strings.TrimSpace(cctx.FormValue("routing_policy", string(RoutingAuto)))))
	if !validPolicy(policy) {
		return cctx.Status(fiber.StatusBadRequest).JSON(Error{Code: ErrInvalidInput, Message: "Unsupported OCR V2 routing policy."})
	}
	owner, _ := cctx.Locals("user_id").(string)
	request := textRequestFromContext(cctx, requestID, ProfileSearchablePDFV2, policy)
	job, execErr := c.service.CreateSearchablePDFJob(cctx.UserContext(), inputs, request, owner)
	if execErr != nil {
		idempotency.Release(cctx, nil)
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: searchablePublicMessage(execErr)})
	}
	if err := idempotency.SetTaskID(cctx, job.JobID, nil); err != nil {
		_, _ = c.service.CancelJob(cctx.UserContext(), job.JobID, owner)
		return cctx.Status(fiber.StatusServiceUnavailable).JSON(Error{Code: ErrTaskStorageUnavailable, Message: "We couldn't save your searchable PDF. Please try again."})
	}
	RecordLanguageSelection(owner, request.Language, false)
	return cctx.Status(fiber.StatusAccepted).JSON(publicJobStatus(job))
}

func (c *Controller) ReplaySearchableJob(cctx *fiber.Ctx, record idempotency.Record) error {
	return cctx.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": record.TaskID, "status": "QUEUED", "profile": ProfileSearchablePDFV2, "result_available": false, "idempotent_replay": true})
}

func (c *Controller) SearchableJobResult(cctx *fiber.Ctx) error {
	artifact, err := c.service.GetOwnedSearchableArtifact(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: searchablePublicMessage(err)})
	}
	cctx.Type("pdf")
	cctx.Set("Content-Type", "application/pdf")
	cctx.Set("Content-Disposition", `attachment; filename="`+safeDownloadName(artifact.Filename)+`"`)
	return cctx.Send(artifact.Bytes)
}

func searchableImageUploads(cctx *fiber.Ctx) ([]*uploads.File, error) {
	context := uploads.FromCtx(cctx)
	if context == nil {
		return nil, errors.New("upload context missing")
	}
	files := context.All("file")
	images := context.All("images")
	if len(files) > 0 && len(images) > 0 {
		return nil, errors.New("use one ordered multipart image field")
	}
	if len(files) == 0 {
		files = images
	}
	if len(files) == 0 {
		return nil, errors.New("missing image uploads")
	}
	return files, nil
}

func textRequestFromContext(cctx *fiber.Ctx, requestID, profile string, policy RoutingPolicy) TextRequest {
	languages := []string{}
	if form, err := cctx.MultipartForm(); err == nil && form != nil {
		languages = append(languages, form.Value["languages"]...)
	}
	languageMode := strings.TrimSpace(cctx.FormValue("language_mode"))
	if languageMode == "" {
		languageMode = "EXPLICIT"
	}
	return TextRequest{
		RequestID:     requestID,
		Profile:       profile,
		Language:      strings.TrimSpace(cctx.FormValue("language")),
		LanguageMode:  languageMode,
		Languages:     languages,
		RoutingPolicy: policy,
	}
}

func safeDownloadName(value string) string {
	name := filepath.Base(strings.TrimSpace(value))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' || r == '/' {
			return '-'
		}
		return r
	}, name)
	if name == "" || name == "." {
		return "document-searchable.pdf"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	return name
}

func (c *Controller) ReplayJob(cctx *fiber.Ctx, record idempotency.Record) error {
	return cctx.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"job_id":            record.TaskID,
		"status":            "QUEUED",
		"profile":           ProfileOCRTextV2,
		"result_available":  false,
		"idempotent_replay": true,
	})
}

func (c *Controller) JobStatus(cctx *fiber.Ctx) error {
	job, err := c.service.GetOwnedJob(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(publicJobStatus(job))
}

func (c *Controller) JobResult(cctx *fiber.Ctx) error {
	result, err := c.service.GetOwnedResult(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(result)
}

func (c *Controller) CancelJob(cctx *fiber.Ctx) error {
	job, err := c.service.CancelJob(cctx.UserContext(), cctx.Params("job_id"), c.owner(cctx))
	if err != nil {
		return cctx.Status(errorStatus(err)).JSON(Error{Code: errorCode(err), Message: publicMessage(err)})
	}
	return cctx.JSON(publicJobStatus(job))
}

func (c *Controller) owner(cctx *fiber.Ctx) string {
	owner, _ := cctx.Locals(identity.LocalUserIDKey).(string)
	return strings.TrimSpace(owner)
}

func publicJobStatus(job *JobStatus) PublicJobStatus {
	status := strings.ToUpper(job.Status)
	if status == "QUEUED" {
		status = "QUEUED"
	} else if status == "RUNNING" || status == "PROCESSING" {
		status = "RUNNING"
	} else if status == "SUCCEEDED" || status == "COMPLETED" {
		status = "SUCCEEDED"
	} else if status == "CANCEL_REQUESTED" || status == "CANCELLED" {
		status = "CANCELLED"
	} else {
		status = "FAILED"
	}
	return PublicJobStatus{
		JobID: job.JobID, Status: status, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Profile: job.Profile,
		Language: job.Language, RoutingPolicy: job.RoutingPolicy, Progress: JobProgress{
			CompletedPages: job.CompletedPages, TotalPages: job.TotalPages, FailedPages: append([]int(nil), job.FailedPages...), CurrentPage: job.CurrentPage, PageStatuses: copyPageStatuses(job.PageStatuses), Percent: job.Progress,
		}, Warnings: append([]string(nil), job.Warnings...), ResultAvailable: job.ResultKey != "",
		Error: publicJobError(job),
	}
}

func copyPageStatuses(statuses map[string]string) map[string]string {
	if len(statuses) == 0 {
		return nil
	}
	copy := make(map[string]string, len(statuses))
	for key, value := range statuses {
		copy[key] = value
	}
	return copy
}

func publicJobError(job *JobStatus) *Error {
	if job.ErrorCode == "" || (strings.ToUpper(job.Status) != "FAILED" && strings.ToUpper(job.Status) != "CANCELLED") {
		return nil
	}
	err := &WorkerError{Code: job.ErrorCode}
	message := publicMessage(err)
	if job.Profile == ProfileSearchablePDFV2 && job.ErrorCode == ErrTaskStorageUnavailable {
		message = "We couldn't save your searchable PDF. Please try again."
	}
	return &Error{Code: job.ErrorCode, Message: message}
}

func searchablePublicMessage(err error) string {
	if errorCode(err) == ErrTaskStorageUnavailable {
		return "We couldn't save your searchable PDF. Please try again."
	}
	return publicMessage(err)
}

func validPolicy(policy RoutingPolicy) bool {
	switch policy {
	case RoutingAuto, RoutingFast, RoutingQuality, RoutingGeometry, RoutingLanguageFallback:
		return true
	default:
		return false
	}
}

func errorCode(err error) string {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr.Code
	}
	var workerErr *WorkerError
	if errors.As(err, &workerErr) {
		return workerErr.Code
	}
	return ErrEngineFailure
}

func errorStatus(err error) int {
	switch errorCode(err) {
	case ErrInvalidInput, ErrCapabilityMismatch, ErrProfileNotEligible, ErrNativeTextUndecided:
		return fiber.StatusBadRequest
	case ErrUnsupportedLanguage:
		return fiber.StatusUnprocessableEntity
	case ErrWorkerAuthentication, ErrEngineFailure, ErrInvalidEngineOutput, ErrPDFRenderFailure, ErrStructuredEngineUnavailable:
		return fiber.StatusBadGateway
	case ErrStructuredOutputInvalid, ErrStructuredProfileNotEligible, ErrTableStructureUnavailable, ErrFormulaStructureUnavailable:
		return fiber.StatusUnprocessableEntity
	case ErrWordGeometryUnavailable, ErrTextNotFound, ErrAnnotationWriteFailure:
		return fiber.StatusUnprocessableEntity
	case ErrTaskStorageUnavailable:
		return fiber.StatusServiceUnavailable
	case ErrNotFound:
		return fiber.StatusNotFound
	case ErrResultNotReady:
		return fiber.StatusConflict
	case "FORBIDDEN":
		return fiber.StatusForbidden
	case ErrResultExpired:
		return fiber.StatusGone
	case ErrEngineUnavailable:
		return fiber.StatusServiceUnavailable
	case ErrTimeout:
		return fiber.StatusGatewayTimeout
	case ErrCancelled:
		return 499
	default:
		return fiber.StatusInternalServerError
	}
}

func publicMessage(err error) string {
	switch errorCode(err) {
	case ErrUnsupportedLanguage:
		return "The requested OCR language is unsupported or not installed."
	case ErrEngineUnavailable:
		return "The requested OCR engine is currently unavailable."
	case ErrTimeout:
		return "OCR processing timed out."
	case ErrCancelled:
		return "OCR processing was cancelled."
	case ErrWorkerAuthentication:
		return "OCR worker authentication failed."
	case ErrInvalidEngineOutput:
		return "OCR worker returned an invalid result."
	case ErrPDFRenderFailure:
		return "The searchable PDF could not be rendered while preserving the source image."
	case ErrStructuredEngineUnavailable:
		return "The structured document engine is currently unavailable."
	case ErrStructuredOutputInvalid:
		return "The structured document result could not be validated."
	case ErrStructuredProfileNotEligible:
		return "The document did not satisfy the structured extraction profile."
	case ErrTableStructureUnavailable:
		return "Table structure is not available for this page."
	case ErrFormulaStructureUnavailable:
		return "Formula structure is not available for this page."
	case ErrWordGeometryUnavailable:
		return "Automatic OCR-aware text selection is unavailable for this document."
	case ErrTextNotFound:
		return "The requested text was not found in the document."
	case ErrAnnotationWriteFailure:
		return "The requested markup could not be written to the PDF."
	case ErrTaskStorageUnavailable:
		return "OCR V2 job persistence is temporarily unavailable."
	case ErrNotFound:
		return "OCR V2 job not found."
	case ErrResultNotReady:
		return "OCR V2 result is not ready."
	case ErrResultExpired:
		return "OCR V2 result is no longer available."
	case "FORBIDDEN":
		return "You are not authorized to access this OCR V2 job."
	default:
		return "OCR V2 could not process the document."
	}
}

func previewPublicMessage(err error) string {
	switch errorCode(err) {
	case ErrUnsupportedLanguage:
		return "The selected language is not available for selectable text."
	case ErrEngineUnavailable:
		return "Selectable text is temporarily unavailable."
	case ErrTimeout:
		return "Preparing selectable text took too long."
	case ErrCancelled:
		return "Selectable text preparation was cancelled."
	case ErrWordGeometryUnavailable:
		return "Selectable text is not available for this document."
	case ErrInvalidInput:
		return "Choose a readable PDF to prepare selectable text."
	default:
		return "Selectable text could not be prepared."
	}
}
