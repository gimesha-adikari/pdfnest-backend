package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockProvider_Identity(t *testing.T) {
	provider := NewMockProvider(nil, nil, 0)
	assert.Equal(t, "mock", provider.Name())
}

func TestMockProvider_SuccessfulSynthesis(t *testing.T) {
	expectedResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-test-1",
		Summary:             "Clean modular microservices architecture",
		ArchitecturePattern: "Microservices",
		KeyComponents: []ComponentDescription{
			{
				Name:    "Fiber Coordinator",
				Role:    "REST API Gateway",
				FactIDs: []string{"TECH-1", "ROUTE-1"},
			},
		},
		DataFlow: []DataFlowStep{
			{
				Step:        1,
				Description: "User submits request",
				FactIDs:     []string{"ROUTE-1"},
			},
		},
		Risks: []RiskItem{
			{
				Category:    "Config",
				Description: "Ensure DB URL is configured",
				FactIDs:     []string{"ENV-1"},
			},
		},
		Model:        "mock-v1",
		InputTokens:  250,
		OutputTokens: 120,
	}

	provider := NewMockProvider(expectedResp, nil, 0)
	ctx := context.Background()

	req := SynthesisRequest{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-test-1",
		SessionID:       "sess-test-1",
		Facts: SafeFactProjection{
			RepositoryName:   "pdfnest",
			PrimaryLanguages: []string{"Go", "TypeScript"},
			Technologies:     []string{"Fiber", "Next.js"},
		},
		MaxOutputTokens: 2048,
		Temperature:     0.2,
	}

	resp, err := provider.Synthesize(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "task-test-1", resp.TaskID)
	assert.Equal(t, "mock", resp.Provider)
	assert.Equal(t, "Clean modular microservices architecture", resp.Summary)
	assert.Len(t, resp.KeyComponents, 1)
	assert.Equal(t, "Fiber Coordinator", resp.KeyComponents[0].Name)
	assert.Equal(t, []string{"TECH-1", "ROUTE-1"}, resp.KeyComponents[0].FactIDs)
	assert.Equal(t, 1, provider.GetCallCount())
	assert.Equal(t, "task-test-1", provider.GetLastRequest().TaskID)
}

func TestMockProvider_ErrorPropagation(t *testing.T) {
	customErr := errors.New("simulated AI provider rate limit")
	provider := NewMockProvider(nil, customErr, 0)

	ctx := context.Background()
	req := SynthesisRequest{TaskID: "task-err"}

	resp, err := provider.Synthesize(ctx, req)
	assert.ErrorIs(t, err, customErr)
	assert.Nil(t, resp)
}

func TestMockProvider_ContextCancellation(t *testing.T) {
	// Configure delay longer than context timeout
	provider := NewMockProvider(nil, nil, 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	resp, err := provider.Synthesize(ctx, SynthesisRequest{TaskID: "task-timeout"})
	duration := time.Since(start)

	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, resp)
	assert.Less(t, duration, 200*time.Millisecond, "Must abort promptly upon context cancellation")
}

func TestMockProvider_RequestIsolation(t *testing.T) {
	provider := NewMockProvider(nil, nil, 0)

	origReq := SynthesisRequest{
		TaskID:    "task-iso",
		SessionID: "sess-iso",
		Facts: SafeFactProjection{
			Technologies: []string{"Go", "PostgreSQL"},
		},
	}

	ctx := context.Background()
	_, err := provider.Synthesize(ctx, origReq)
	require.NoError(t, err)

	// Verify original request values were not mutated
	assert.Equal(t, "task-iso", origReq.TaskID)
	assert.Equal(t, []string{"Go", "PostgreSQL"}, origReq.Facts.Technologies)
}

func TestMockProvider_Determinism(t *testing.T) {
	template := &SynthesisResponse{
		Summary:             "Deterministic summary",
		ArchitecturePattern: "Monolith",
	}
	provider := NewMockProvider(template, nil, 0)
	ctx := context.Background()
	req := SynthesisRequest{TaskID: "task-det"}

	resp1, err1 := provider.Synthesize(ctx, req)
	require.NoError(t, err1)

	resp2, err2 := provider.Synthesize(ctx, req)
	require.NoError(t, err2)

	assert.Equal(t, resp1.Summary, resp2.Summary)
	assert.Equal(t, resp1.ArchitecturePattern, resp2.ArchitecturePattern)
}

func TestMockProvider_ConcurrentRaceDetector(t *testing.T) {
	provider := NewMockProvider(nil, nil, 5*time.Millisecond)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := SynthesisRequest{
				TaskID:    "task-concurrent",
				SessionID: "sess-concurrent",
			}
			resp, err := provider.Synthesize(ctx, req)
			assert.NoError(t, err)
			assert.NotNil(t, resp)
		}(i)
	}

	wg.Wait()
	assert.Equal(t, 20, provider.GetCallCount())
}
