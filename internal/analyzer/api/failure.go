package api

import (
	"errors"
	"pdfnest-backend/internal/analyzer/worker"
)

// Only this service-created failure carries a public message. A wrapper around
// ErrAnalysisFailed alone never grants permission to expose its error string.
type analysisFailure struct{ message string }

func (e *analysisFailure) Error() string {
	return ErrAnalysisFailed.Error() + ": " + worker.SafeStoredFailureMessage(e.message)
}
func (e *analysisFailure) Unwrap() error { return ErrAnalysisFailed }
func publicAnalysisFailure(err error) string {
	var failure *analysisFailure
	if errors.As(err, &failure) {
		return worker.SafeStoredFailureMessage(failure.message)
	}
	return worker.PublicFailureMessage(nil)
}
