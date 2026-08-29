package ocrv2

import (
	"context"
	"errors"
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
	request := TextRequest{RequestID: requestID, Profile: ProfileOCRTextV2, Language: strings.TrimSpace(cctx.FormValue("language")), RoutingPolicy: policy}
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
	request := TextRequest{RequestID: requestID, Profile: ProfileOCRTextV2, Language: strings.TrimSpace(cctx.FormValue("language")), RoutingPolicy: policy}
	job, execErr := c.service.CreateJob(cctx.UserContext(), upload.Path, request, owner)
	if execErr != nil {
		idempotency.Release(cctx, nil)
		return cctx.Status(errorStatus(execErr)).JSON(Error{Code: errorCode(execErr), Message: publicMessage(execErr)})
	}
	if err := idempotency.SetTaskID(cctx, job.JobID, nil); err != nil {
		_, _ = c.service.CancelJob(cctx.UserContext(), job.JobID, owner)
		return cctx.Status(fiber.StatusServiceUnavailable).JSON(Error{Code: ErrTaskStorageUnavailable, Message: "OCR V2 job persistence is temporarily unavailable."})
	}
	return cctx.Status(fiber.StatusAccepted).JSON(publicJobStatus(job))
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
	return &Error{Code: job.ErrorCode, Message: publicMessage(&WorkerError{Code: job.ErrorCode})}
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
	case ErrWorkerAuthentication, ErrEngineFailure, ErrInvalidEngineOutput:
		return fiber.StatusBadGateway
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
