package api

import (
	"fmt"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/worker"
)

const (
	MaxCustomPatterns = 100
	MaxForceIncludes  = 100
	MaxPatternLength  = 256
)

// ScopeConfigAdapter converts and validates API scope requests into engine/worker configuration.
type ScopeConfigAdapter struct{}

// NewScopeConfigAdapter instantiates a ScopeConfigAdapter.
func NewScopeConfigAdapter() *ScopeConfigAdapter {
	return &ScopeConfigAdapter{}
}

// AdaptAndValidate validates pattern boundaries and converts UpdateScopeRequest to worker.ScopeConfig and scopeHash.
func (a *ScopeConfigAdapter) AdaptAndValidate(req UpdateScopeRequest) (worker.ScopeConfig, string, error) {
	if len(req.CustomPatterns) > MaxCustomPatterns {
		return worker.ScopeConfig{}, "", fmt.Errorf("custom patterns exceed maximum limit of %d", MaxCustomPatterns)
	}
	if len(req.ForceIncludes) > MaxForceIncludes {
		return worker.ScopeConfig{}, "", fmt.Errorf("force includes exceed maximum limit of %d", MaxForceIncludes)
	}

	sanitizedCustom := make([]string, 0, len(req.CustomPatterns))
	for _, p := range req.CustomPatterns {
		clean := strings.TrimSpace(p)
		if clean == "" {
			continue
		}
		if len(clean) > MaxPatternLength {
			return worker.ScopeConfig{}, "", fmt.Errorf("pattern '%s' exceeds maximum length of %d", clean, MaxPatternLength)
		}
		if strings.ContainsRune(clean, '\x00') {
			return worker.ScopeConfig{}, "", fmt.Errorf("pattern contains invalid null bytes")
		}
		sanitizedCustom = append(sanitizedCustom, clean)
	}

	sanitizedForce := make([]string, 0, len(req.ForceIncludes))
	for _, p := range req.ForceIncludes {
		clean := strings.TrimSpace(p)
		if clean == "" {
			continue
		}
		if len(clean) > MaxPatternLength {
			return worker.ScopeConfig{}, "", fmt.Errorf("force include pattern '%s' exceeds maximum length of %d", clean, MaxPatternLength)
		}
		if strings.ContainsRune(clean, '\x00') {
			return worker.ScopeConfig{}, "", fmt.Errorf("pattern contains invalid null bytes")
		}
		sanitizedForce = append(sanitizedForce, clean)
	}

	scopeConfig := worker.ScopeConfig{
		CustomPatterns: sanitizedCustom,
		EnabledPresets: req.EnabledPresets,
		ForceIncludes:  sanitizedForce,
		GitignoreRules: req.GitignoreRules,
	}

	hashInput := engine.ScopeHashInput{
		CustomExclusions: sanitizedCustom,
		EnabledPresets:   req.EnabledPresets,
		ForceIncludes:    sanitizedForce,
		SelectedDomains:  req.SelectedDomains,
	}
	scopeHash := engine.ComputeScopeHash(hashInput)

	return scopeConfig, scopeHash, nil
}

// TaskProgressAdapter converts internal worker.TaskProgress into public API TaskStatusResponse.
type TaskProgressAdapter struct{}

// NewTaskProgressAdapter instantiates a TaskProgressAdapter.
func NewTaskProgressAdapter() *TaskProgressAdapter {
	return &TaskProgressAdapter{}
}

// Adapt transforms worker.TaskProgress into public TaskStatusResponse.
func (a *TaskProgressAdapter) Adapt(p worker.TaskProgress) TaskStatusResponse {
	return TaskStatusResponse{
		TaskID:          p.TaskID,
		SessionID:       p.SessionID,
		Status:          p.Status,
		ProgressPercent: p.ProgressPercent,
		StageMessage:    p.StageMessage,
		ErrorMessage:    p.ErrorMessage,
		UpdatedAt:       p.UpdatedAt,
	}
}
