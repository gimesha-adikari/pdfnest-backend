package ai

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
)

func TestFullAIPipeline(t *testing.T) {
	canonical := createTestCanonicalResult()
	// Add some graph and evidence to make projection work nicely
	canonical.Technologies = []engine.TechnologyItem{
		{Name: "Fiber", Category: "framework", Confidence: "confirmed"},
		{Name: "PostgreSQL", Category: "database", Confidence: "confirmed"},
	}
	canonical.Routes = []engine.ApiRouteItem{
		{Method: "GET", Path: "/api/v1/health"},
	}

	mockResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-integration",
		Summary:             "Go Fiber service with PostgreSQL database",
		ArchitecturePattern: "Monolith",
		KeyComponents: []ComponentDescription{
			{Name: "Fiber API", Role: "Web Router", FactIDs: []string{"TECH-2", "ROUTE-1"}},
			{Name: "Redis Cache", Role: "Cache", FactIDs: []string{"TECH-999"}}, // Hallucination!
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
		"task-integration",
		"sess-integration",
		true,
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, valRes)

	// Valid, but rejected some claims
	assert.True(t, valRes.Valid)
	assert.Equal(t, 1, valRes.RejectedClaims)

	// Should only have 1 key component after hallucination is stripped
	assert.Len(t, resp.KeyComponents, 1)
	assert.Equal(t, "Fiber API", resp.KeyComponents[0].Name)

	// Ensure AI is attached
	require.NotNil(t, canonical.AI)
	assert.Equal(t, resp, canonical.AI)
}
