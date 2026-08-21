package ai

import (
	"context"
	"sync"
	"time"
)

// MockProvider is a deterministic, fully isolated mock implementation of the Provider interface.
// It never executes network calls, accesses files, or relies on external AI services.
type MockProvider struct {
	mu        sync.RWMutex
	response  *SynthesisResponse
	err       error
	delay     time.Duration
	name      string
	callCount int
	lastReq   *SynthesisRequest
}

// NewMockProvider creates a new MockProvider with configured response, error, and delay parameters.
func NewMockProvider(resp *SynthesisResponse, err error, delay time.Duration) *MockProvider {
	return &MockProvider{
		response: resp,
		err:      err,
		delay:    delay,
		name:     "mock",
	}
}

// Synthesize returns the configured mock response or error, strictly respecting context cancellation.
func (m *MockProvider) Synthesize(ctx context.Context, req SynthesisRequest) (*SynthesisResponse, error) {
	m.mu.Lock()
	m.callCount++
	reqCopy := req
	m.lastReq = &reqCopy
	delay := m.delay
	configuredErr := m.err
	respTemplate := m.response
	m.mu.Unlock()

	// 1. Context Cancellation Check
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 2. Simulated Delay with Cancellation Support
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	// 3. Configured Error Handling
	if configuredErr != nil {
		return nil, configuredErr
	}

	// 4. Clean Deterministic Response Copy
	if respTemplate == nil {
		return &SynthesisResponse{
			ProtocolVersion:     "1.0.0",
			TaskID:              req.TaskID,
			Summary:             "Default deterministic mock architectural summary",
			ArchitecturePattern: "Monolithic",
			Provider:            "mock",
			Model:               "mock-v1",
			InputTokens:         100,
			OutputTokens:        50,
			DurationMs:          delay.Milliseconds(),
		}, nil
	}

	// Return deep clone of response template
	cloned := *respTemplate
	cloned.TaskID = req.TaskID
	cloned.Provider = "mock"

	// Clone slices to guarantee isolation
	if respTemplate.KeyComponents != nil {
		cloned.KeyComponents = make([]ComponentDescription, len(respTemplate.KeyComponents))
		copy(cloned.KeyComponents, respTemplate.KeyComponents)
	}
	if respTemplate.DataFlow != nil {
		cloned.DataFlow = make([]DataFlowStep, len(respTemplate.DataFlow))
		copy(cloned.DataFlow, respTemplate.DataFlow)
	}
	if respTemplate.Risks != nil {
		cloned.Risks = make([]RiskItem, len(respTemplate.Risks))
		copy(cloned.Risks, respTemplate.Risks)
	}

	return &cloned, nil
}

// Name returns the identifier of the mock provider.
func (m *MockProvider) Name() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.name != "" {
		return m.name
	}
	return "mock"
}

// GetCallCount returns the number of times Synthesize has been invoked.
func (m *MockProvider) GetCallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.callCount
}

// GetLastRequest returns a copy of the most recently received SynthesisRequest.
func (m *MockProvider) GetLastRequest() *SynthesisRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lastReq == nil {
		return nil
	}
	copyReq := *m.lastReq
	return &copyReq
}
