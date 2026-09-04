package ocrv2

import "fmt"

const (
	ErrInvalidInput                 = "INVALID_INPUT"
	ErrUnsupportedLanguage          = "UNSUPPORTED_LANGUAGE"
	ErrLanguageDetectionUncertain   = "LANGUAGE_DETECTION_UNCERTAIN"
	ErrEngineUnavailable            = "ENGINE_UNAVAILABLE"
	ErrEngineFailure                = "ENGINE_FAILURE"
	ErrTimeout                      = "TIMEOUT"
	ErrCancelled                    = "CANCELLED"
	ErrInvalidEngineOutput          = "INVALID_ENGINE_OUTPUT"
	ErrCapabilityMismatch           = "CAPABILITY_MISMATCH"
	ErrProfileNotEligible           = "PROFILE_NOT_ELIGIBLE"
	ErrNativeTextUndecided          = "NATIVE_TEXT_UNDECIDED"
	ErrWorkerAuthentication         = "WORKER_AUTHENTICATION_FAILED"
	ErrTaskStorageUnavailable       = "TASK_STORAGE_UNAVAILABLE"
	ErrNotFound                     = "NOT_FOUND"
	ErrResultNotReady               = "RESULT_NOT_READY"
	ErrResultExpired                = "RESULT_EXPIRED"
	ErrPDFRenderFailure             = "PDF_RENDER_FAILURE"
	ErrStructuredEngineUnavailable  = "STRUCTURED_ENGINE_UNAVAILABLE"
	ErrStructuredOutputInvalid      = "STRUCTURED_OUTPUT_INVALID"
	ErrStructuredProfileNotEligible = "STRUCTURED_PROFILE_NOT_ELIGIBLE"
	ErrTableStructureUnavailable    = "TABLE_STRUCTURE_UNAVAILABLE"
	ErrFormulaStructureUnavailable  = "FORMULA_STRUCTURE_UNAVAILABLE"
	ErrWordGeometryUnavailable      = "WORD_GEOMETRY_NOT_AVAILABLE"
	ErrTextNotFound                 = "TEXT_NOT_FOUND"
	ErrAnnotationWriteFailure       = "ANNOTATION_WRITE_FAILURE"
)

type RequestError struct {
	Code string
}

func (e *RequestError) Error() string { return e.Code }

type WorkerError struct {
	Code       string
	HTTPStatus int
	Message    string
	Response   *TextResponse
}

func (e *WorkerError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
