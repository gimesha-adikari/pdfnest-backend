package ai

import (
	"context"
	"fmt"

	"pdfnest-backend/internal/analyzer/engine"
)

// SynthesizeArchitectureSummary orchestrates safe, single-shot, provider-neutral AI architecture synthesis.
// It strictly respects user consent, timeout constraints, canonical immutability, and fail-closed validation.
// Any failure in the AI layer is non-fatal and preserves the integrity of CanonicalAnalysisResult.
func SynthesizeArchitectureSummary(
	ctx context.Context,
	cfg Config,
	provider Provider,
	canonical *engine.CanonicalAnalysisResult,
	taskID string,
	sessionID string,
	jobEnableAI bool,
) (*SynthesisResponse, *ValidationResult, error) {
	// 1. Consent & Feature Flag Enforcement
	if !cfg.Enabled || !jobEnableAI {
		return nil, nil, nil
	}

	if canonical == nil {
		return nil, nil, fmt.Errorf("canonical result is nil")
	}

	// 2. Resolve Provider Instance
	p := provider
	if p == nil {
		var err error
		p, err = NewProvider(cfg)
		if err != nil {
			valRes := ValidationResult{
				Valid:            false,
				RejectionReasons: []string{fmt.Sprintf("provider init failed: %v", err)},
			}
			return nil, &valRes, nil
		}
	}

	// 3. Extract Allowlisted Safe Fact Projection & Fact Catalog (7C-B)
	projection, catalog, err := BuildSafeFactProjection(canonical)
	if err != nil {
		valRes := ValidationResult{
			Valid:            false,
			RejectionReasons: []string{fmt.Sprintf("safe projection failed: %v", err)},
		}
		return nil, &valRes, nil
	}

	// 4. Construct Provider Request (7C-A Contract)
	req := SynthesisRequest{
		ProtocolVersion: "1.0.0",
		TaskID:          taskID,
		SessionID:       sessionID,
		Facts:           projection,
		Catalog:         catalog,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Temperature:     cfg.Temperature,
	}

	// 5. Enforce Hard Timeout (Default 15s)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	synthesisCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 6. Single-Shot Provider Invocation (Zero agent loops, zero recursive retries)
	rawResponse, synthErr := p.Synthesize(synthesisCtx, req)
	if synthErr != nil {
		errClass := "provider error"
		if synthesisCtx.Err() == context.DeadlineExceeded {
			errClass = "provider timeout"
		} else if synthesisCtx.Err() == context.Canceled {
			errClass = "context canceled"
		}

		valRes := ValidationResult{
			Valid:            false,
			RejectionReasons: []string{fmt.Sprintf("%s: %v", errClass, synthErr)},
		}
		return nil, &valRes, nil
	}

	// 7. Rigorous Response Validation & Anti-Hallucination Grounding (7C-C)
	validatedResponse, valResult, valErr := ValidateSynthesisResponse(rawResponse, &catalog, taskID)
	if valErr != nil || !valResult.Valid {
		return nil, &valResult, nil
	}

	// 8. Return Validated Architecture Summary
	canonical.AI = validatedResponse
	return validatedResponse, &valResult, nil
}
