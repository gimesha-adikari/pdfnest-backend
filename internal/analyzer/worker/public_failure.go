package worker

import (
	"context"
	"errors"

	"pdfnest-backend/internal/analyzer/engine/acquisition"
)

// PublicFailureMessage selects only application-owned text. Error strings,
// including output wrapped with a trusted sentinel, are never public messages.
// In particular, quota-like stderr is not evidence of a resource-limit failure.
func PublicFailureMessage(err error) string {
	switch {
	case errors.Is(err, acquisition.ErrGitResourceLimit):
		return "Repository exceeds Git acquisition resource limits."
	case errors.Is(err, acquisition.ErrGitTimeout):
		return "Repository acquisition timed out."
	case errors.Is(err, acquisition.ErrGitAuthRequired):
		return "Repository authentication is required."
	case errors.Is(err, acquisition.ErrGitUnreachable):
		return "Could not connect to the repository host."
	case errors.Is(err, acquisition.ErrGitInvalidRepository):
		return "Repository was not found or is inaccessible."
	case errors.Is(err, acquisition.ErrSSRFBlocked):
		return "Repository host is not permitted."
	case errors.Is(err, acquisition.ErrUnsupportedGitProtocol):
		return "Repository URL must use HTTPS."
	case errors.Is(err, acquisition.ErrGitAcquisitionFailed), errors.Is(err, acquisition.ErrGitCloneFailed):
		return "Repository acquisition failed."
	case errors.Is(err, context.DeadlineExceeded):
		return "Repository analysis timed out."
	case errors.Is(err, context.Canceled):
		return "Repository analysis was canceled."
	default:
		return "Repository analysis failed."
	}
}

// SafeStoredFailureMessage is a closed vocabulary, not a diagnostic parser.
// Old records have no typed error provenance: preserve only exact approved
// messages, otherwise use a generic failure without guessing a classification.
func SafeStoredFailureMessage(message string) string {
	switch message {
	case "Repository exceeds Git acquisition resource limits.",
		"Repository acquisition timed out.",
		"Repository authentication is required.",
		"Could not connect to the repository host.",
		"Repository was not found or is inaccessible.",
		"Repository host is not permitted.",
		"Repository URL must use HTTPS.",
		"Repository acquisition failed.",
		"Repository analysis timed out.",
		"Repository analysis was canceled.",
		"Repository analysis service is currently unavailable. No active analyzer workers are running.",
		"Analysis task exceeded maximum queue wait timeout (10 minutes).",
		"Analyzer worker processing this repository terminated unexpectedly.":
		return message
	default:
		return "Repository analysis failed."
	}
}

// PublicTaskProgress applies at writes and reads, including legacy Redis data.
// Keep terminal status/identity/timestamps, but never publish diagnostic stages
// or an embedded result from a failed execution.
func PublicTaskProgress(progress TaskProgress) TaskProgress {
	if progress.Status == StatusFailed {
		progress.StageMessage = "Analysis failed"
		progress.ErrorMessage = SafeStoredFailureMessage(progress.ErrorMessage)
		progress.Result = nil
	} else if progress.ErrorMessage != "" {
		progress.ErrorMessage = SafeStoredFailureMessage(progress.ErrorMessage)
	}
	return progress
}
