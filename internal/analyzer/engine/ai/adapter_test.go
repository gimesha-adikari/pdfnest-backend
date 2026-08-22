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

func TestSynthesizeArchitectureSummary_EmptyGeminiKeySafeFailure(t *testing.T) {
	canonical := createTestCanonicalResult()
	// When AI is enabled with Gemini but GEMINI_API_KEY is empty, it must fail safely without panic or error
	cfg := Config{
		Enabled:      true,
		Provider:     "gemini",
		GeminiAPIKey: "", // empty key
	}

	resp, valRes, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		nil, // dynamically resolve provider from cfg
		canonical,
		"task-empty-key",
		"sess-empty-key",
		true,
	)

	// Fails safely: non-fatal, err is nil, resp is nil, valRes records authentication/init failure
	require.NoError(t, err)
	assert.Nil(t, resp)
	require.NotNil(t, valRes)
	assert.False(t, valRes.Valid)
	assert.NotEmpty(t, valRes.RejectionReasons)
	assert.Contains(t, valRes.RejectionReasons[0], "GEMINI_API_KEY is required")
	assert.Nil(t, canonical.AI)
}

func TestSynthesizeArchitectureSummary_Success(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-1",
		Summary:             "Go Fiber service with PostgreSQL database",
		ArchitecturePattern: "Monolith",
		KeyComponents: []ComponentDescription{
			{Name: "Fiber API", Role: "Web Router", FactIDs: []string{"TECH-2", "ROUTE-1"}},
			{Name: "PostgreSQL Database", Role: "Relational Storage", FactIDs: []string{"TECH-1"}},
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

	// Verify that the provider received the populated FactCatalog
	lastReq := mockP.GetLastRequest()
	require.NotNil(t, lastReq)
	assert.NotEmpty(t, lastReq.Catalog.Facts)
	assert.Equal(t, len(lastReq.Catalog.Facts), lastReq.Catalog.TotalFactsCount)
	assert.Contains(t, lastReq.Catalog.FactMap, "TECH-1")
	assert.Contains(t, lastReq.Catalog.FactMap, "TECH-2")
	assert.Contains(t, lastReq.Catalog.FactMap, "ROUTE-1")
	assert.Contains(t, lastReq.Catalog.FactMap, "ENV-1")
}

func TestSynthesizeArchitectureSummary_FactCatalogPropagation(t *testing.T) {
	canonical := createTestCanonicalResult()
	mockP := NewMockProvider(nil, nil, 0)
	cfg := Config{Enabled: true, Timeout: 5 * time.Second}

	_, _, err := SynthesizeArchitectureSummary(
		context.Background(),
		cfg,
		mockP,
		canonical,
		"task-cat-test",
		"sess-cat-test",
		true,
	)
	require.NoError(t, err)

	lastReq := mockP.GetLastRequest()
	require.NotNil(t, lastReq)
	assert.Equal(t, "task-cat-test", lastReq.TaskID)
	assert.NotEmpty(t, lastReq.Catalog.Facts)
	assert.Equal(t, len(lastReq.Catalog.Facts), lastReq.Catalog.TotalFactsCount)

	// Verify prompt payload built from request contains the deterministic Fact IDs
	promptPayload, promptErr := BuildPromptPayload(lastReq.Facts, lastReq.Catalog, DefaultMaxPromptBytes)
	require.NoError(t, promptErr)
	assert.Contains(t, promptPayload.UserData, "TECH-1")
	assert.Contains(t, promptPayload.UserData, "TECH-2")
	assert.Contains(t, promptPayload.UserData, "ROUTE-1")
	assert.Contains(t, promptPayload.UserData, "ENV-1")
}

func TestSynthesizeArchitectureSummary_MismatchedFactIDGrounding(t *testing.T) {
	canonical := createTestCanonicalResult()
	// TECH-1 is PostgreSQL, TECH-2 is Fiber
	mockResp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-grounding",
		Summary:             "Test summary",
		ArchitecturePattern: "Monolith",
		KeyComponents: []ComponentDescription{
			{
				Name:    "PostgreSQL Relational DB",
				Role:    "Relational database storage",
				FactIDs: []string{"TECH-2"}, // TECH-2 is Fiber, NOT PostgreSQL!
			},
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
		"task-grounding",
		"sess-grounding",
		true,
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, valRes)
	assert.True(t, valRes.Valid)
	assert.Equal(t, 1, valRes.RejectedClaims)
	assert.Empty(t, resp.KeyComponents, "Claim asserting PostgreSQL while citing TECH-2 (Fiber) must be rejected as ungrounded")
}

func TestSynthesizeArchitectureSummary_Determinism(t *testing.T) {
	canonical1 := createTestCanonicalResult()
	canonical2 := createTestCanonicalResult()

	proj1, cat1, err1 := BuildSafeFactProjection(canonical1)
	require.NoError(t, err1)

	proj2, cat2, err2 := BuildSafeFactProjection(canonical2)
	require.NoError(t, err2)

	assert.Equal(t, len(cat1.Facts), len(cat2.Facts))
	for i := range cat1.Facts {
		assert.Equal(t, cat1.Facts[i].ID, cat2.Facts[i].ID)
		assert.Equal(t, cat1.Facts[i].Value, cat2.Facts[i].Value)
		assert.Equal(t, cat1.Facts[i].Category, cat2.Facts[i].Category)
	}
	assert.Equal(t, proj1.RepositoryName, proj2.RepositoryName)
	assert.Equal(t, proj1.Technologies, proj2.Technologies)
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
			{Name: "Fiber API", Role: "Web Router", FactIDs: []string{"TECH-2", "ROUTE-1"}},
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
