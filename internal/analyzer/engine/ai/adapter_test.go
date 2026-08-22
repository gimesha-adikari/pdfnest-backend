package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
)

func createTestCanonicalResult() *engine.CanonicalAnalysisResult {
	res := engine.NewEmptyCanonicalResult("sess-1", "test-repo", engine.SourceTypeGit)
	res.CreatedAt = time.Now()
	res.Technologies = []engine.TechnologyItem{
		{Name: "Fiber", Category: "framework", Confidence: "confirmed"},
		{Name: "PostgreSQL", Category: "database", Confidence: "confirmed"},
	}
	res.Routes = []engine.ApiRouteItem{
		{Method: "GET", Path: "/api/v1/health"},
	}
	res.Environment.Variables = []engine.EnvironmentVariable{
		{Name: "DATABASE_URL", Required: true},
	}
	return res
}

func TestSynthesizeArchitectureSummary_Disabled(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, nil, 0)
	cfg := Config{Enabled: false}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true, // job requested AI, but global config is disabled
	)

	require.NoError(t, err)
	assert.Nil(t, resp)
	assert.Nil(t, valRes)
	assert.Equal(t, 0, mockP.GetCallCount(), "Provider must never be called when AI is disabled")
}

func TestSynthesizeArchitectureSummary_ConsentCheck(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, nil, 0)
	cfg := Config{Enabled: true}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		false, // user opted out of AI
	)

	require.NoError(t, err)
	assert.Nil(t, resp)
	assert.Nil(t, valRes)
	assert.Equal(t, 0, mockP.GetCallCount(), "Provider must never be called when user opted out")
}

func TestSynthesizeArchitectureSummary_Success(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-1",
		Summary:             "Go Fiber service with PostgreSQL database",
		ArchitecturePattern: "Monolith",
		KeyComponents: []ComponentDescription{
			{Name: "Fiber API", Role: "Web Router", FactIDs: []string{"TECH-1", "ROUTE-1"}},
		},
		Model: "mock-v1",
	}
	mockP := NewMockProvider(mockResp, nil, 0)
	cfg := Config{Enabled: true, Timeout: 5 * time.Second}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, valRes)
	assert.True(t, valRes.Valid)
	assert.Equal(t, "Go Fiber service with PostgreSQL database", resp.Summary)
	assert.Equal(t, 1, mockP.GetCallCount())
}

func TestSynthesizeArchitectureSummary_TimeoutNonFatal(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, nil, 500*time.Millisecond) // delay longer than timeout
	cfg := Config{Enabled: true, Timeout: 20 * time.Millisecond}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)

	// Failure is non-fatal: err is nil, resp is nil, valRes documents rejection
	require.NoError(t, err)
	assert.Nil(t, resp)
	require.NotNil(t, valRes)
	assert.False(t, valRes.Valid)
	assert.Contains(t, valRes.RejectionReasons[0], "timeout")

	// Canonical facts remain intact
	assert.Equal(t, "Fiber", canonical.Technologies[0].Name)
}

func TestSynthesizeArchitectureSummary_ProviderErrorNonFatal(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, errors.New("upstream AI 429 rate limit"), 0)
	cfg := Config{Enabled: true, Timeout: 5 * time.Second}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)

	require.NoError(t, err)
	assert.Nil(t, resp)
	require.NotNil(t, valRes)
	assert.False(t, valRes.Valid)
	assert.Contains(t, valRes.RejectionReasons[0], "429 rate limit")
}

func TestSynthesizeArchitectureSummary_HallucinationRejected(t *testing.T) {
	canonical := createTestCanonicalResult() // Only has Fiber, PostgreSQL
	mockResp := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-1",
		Summary:         "Summary",
		KeyComponents: []ComponentDescription{
			{Name: "Django Core", Role: "Django Web Framework", FactIDs: []string{"TECH-1"}}, // Hallucination!
		},
	}
	mockP := NewMockProvider(mockResp, nil, 0)
	cfg := Config{Enabled: true, Timeout: 5 * time.Second}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, valRes)
	assert.Equal(t, 1, valRes.RejectedClaims)
	assert.Empty(t, resp.KeyComponents, "Must strip hallucinated Django component")
}

func TestSynthesizeArchitectureSummary_CanonicalImmutability(t *testing.T) {
	canonical := createTestCanonicalResult()
	origTechCount := len(canonical.Technologies)
	origRouteCount := len(canonical.Routes)

	mockP := NewMockProvider(nil, nil, 0)
	cfg := Config{Enabled: true}

	_, _, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)
	require.NoError(t, err)

	assert.Equal(t, origTechCount, len(canonical.Technologies))
	assert.Equal(t, origRouteCount, len(canonical.Routes))
	assert.Equal(t, "Fiber", canonical.Technologies[0].Name)
}

func TestSynthesizeArchitectureSummary_ConcurrentRaceSafety(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, nil, 2*time.Millisecond)
	cfg := Config{Enabled: true, Timeout: 2 * time.Second}

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			resp, _, err := SynthesizeArchitectureSummary(
				context.Background(),
				cfg,
				mockP,
				canonical,
				"task-concurrent",
				"sess-concurrent",
				true,
			)
			assert.NoError(t, err)
			assert.NotNil(t, resp)
		}(i)
	}

	wg.Wait()
	assert.Equal(t, 15, mockP.GetCallCount())
}

func TestPipelineIntegrationAdapter(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-1",
		Summary:             "Go Fiber service with PostgreSQL database",
		ArchitecturePattern: "Monolith",
		KeyComponents: []ComponentDescription{
			{Name: "Fiber API", Role: "Web Router", FactIDs: []string{"TECH-1", "ROUTE-1"}},
		},
		Model: "mock-v1",
	}
	mockP := NewMockProvider(mockResp, nil, 0)
	cfg := Config{Enabled: true, Timeout: 5 * time.Second}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-1",
		"sess-1",
		true,
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, valRes)
	assert.True(t, valRes.Valid)
	assert.Equal(t, "Go Fiber service with PostgreSQL database", resp.Summary)
	assert.Equal(t, 1, mockP.GetCallCount())
	assert.NotNil(t, canonical.AI)
	assert.Equal(t, resp, canonical.AI)
}
