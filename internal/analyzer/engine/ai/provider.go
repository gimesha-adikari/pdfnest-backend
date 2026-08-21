package ai

import (
	"context"
	"errors"
)

// Provider-neutral error definitions for AI synthesis operations.
var (
	ErrProviderUnavailable          = errors.New("ai: provider service is unavailable")
	ErrProviderTimeout              = errors.New("ai: provider request timed out")
	ErrProviderRateLimited          = errors.New("ai: provider rate limit exceeded")
	ErrProviderInvalidResponse      = errors.New("ai: provider returned invalid response structure")
	ErrProviderAuthenticationFailed = errors.New("ai: provider authentication failed")
	ErrProviderContextExceeded      = errors.New("ai: input token context window exceeded")
	ErrInvalidSynthesisRequest      = errors.New("ai: invalid synthesis request payload")
)

// ComponentDescription captures an architectural component derived from canonical facts.
type ComponentDescription struct {
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	FactIDs []string `json:"factIds,omitempty"`
}

// DataFlowStep describes a single data or control flow stage in the repository architecture.
type DataFlowStep struct {
	Step        int      `json:"step"`
	Description string   `json:"description"`
	FactIDs     []string `json:"factIds,omitempty"`
}

// RiskItem describes an architectural, configuration, or operational risk factor.
type RiskItem struct {
	Category    string   `json:"category"`
	Description string   `json:"description"`
	FactIDs     []string `json:"factIds,omitempty"`
}

// SafeFactProjection encapsulates a sanitized, metadata-only projection of canonical facts.
// It never contains raw repository file blobs, source contents, or secret values.
type SafeFactProjection struct {
	RepositoryName       string   `json:"repositoryName"`
	PrimaryLanguages     []string `json:"primaryLanguages"`
	Technologies         []string `json:"technologies"`
	Endpoints            []string `json:"endpoints"`
	Models               []string `json:"models"`
	EnvironmentVariables []string `json:"environmentVariables"`
	TestingFrameworks    []string `json:"testingFrameworks"`
	DeploymentSystems    []string `json:"deploymentSystems"`
}

// SynthesisRequest encapsulates the provider-neutral input contract for AI architecture synthesis.
type SynthesisRequest struct {
	ProtocolVersion string             `json:"protocolVersion"`
	TaskID          string             `json:"taskId"`
	SessionID       string             `json:"sessionId"`
	Facts           SafeFactProjection `json:"facts"`
	MaxOutputTokens int                `json:"maxOutputTokens"`
	Temperature     float32            `json:"temperature"`
}

// SynthesisResponse encapsulates the structured, provider-neutral architecture summary output.
type SynthesisResponse struct {
	ProtocolVersion     string                 `json:"protocolVersion"`
	TaskID              string                 `json:"taskId"`
	Summary             string                 `json:"summary"`
	ArchitecturePattern string                 `json:"architecturePattern"`
	KeyComponents       []ComponentDescription `json:"keyComponents"`
	DataFlow            []DataFlowStep         `json:"dataFlow"`
	Risks               []RiskItem             `json:"risks"`

	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	DurationMs   int64  `json:"durationMs"`
}

// Provider defines the neutral contract for AI architecture synthesis engines.
type Provider interface {
	// Synthesize generates a structured architecture synthesis from sanitized canonical facts.
	Synthesize(ctx context.Context, req SynthesisRequest) (*SynthesisResponse, error)

	// Name returns the identifier of the synthesis provider.
	Name() string
}
