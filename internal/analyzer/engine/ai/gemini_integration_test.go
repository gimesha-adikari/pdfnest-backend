package ai

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
)

// TestRealGeminiIntegration executes a live end-to-end AI synthesis request against the Google Gemini API.
// It is strictly opt-in and offline by default unless RUN_GEMINI_INTEGRATION=1 is explicitly set and GEMINI_API_KEY is available.
func TestRealGeminiIntegration(t *testing.T) {
	if os.Getenv("RUN_GEMINI_INTEGRATION") != "1" {
		t.Skip("Skipping live Gemini integration test. Set RUN_GEMINI_INTEGRATION=1 to run.")
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping live Gemini integration test: GEMINI_API_KEY environment variable is not set.")
	}

	// 1. Construct minimal deterministic CanonicalAnalysisResult
	canonical := engine.NewEmptyCanonicalResult("sess-live-smoke", "platen-demo", engine.SourceTypeGit)
	canonical.CreatedAt = time.Now()
	canonical.Metrics.Languages = []engine.LanguageMetric{
		{Name: "Go", Percentage: 85.0, FileCount: 40, Bytes: 150000},
		{Name: "TypeScript", Percentage: 15.0, FileCount: 10, Bytes: 30000},
	}
	canonical.Technologies = []engine.TechnologyItem{
		{Name: "Fiber", Category: "framework", Confidence: "confirmed"},
		{Name: "PostgreSQL", Category: "database", Confidence: "confirmed"},
		{Name: "Redis", Category: "database", Confidence: "probable"},
	}
	canonical.Routes = []engine.ApiRouteItem{
		{Method: "POST", Path: "/api/v1/analyze"},
		{Method: "GET", Path: "/api/v1/tasks/:id"},
	}
	canonical.Environment.Variables = []engine.EnvironmentVariable{
		{Name: "PORT", Required: false, InferredType: "int"},
		{Name: "DATABASE_URL", Required: true, InferredType: "string"},
		{Name: "REDIS_URL", Required: true, InferredType: "url"},
	}
	canonical.Testing.Frameworks = []string{"Go Test"}
	canonical.Deployment.DockerAvailable = true

	// 2. Build real SafeFactProjection & FactCatalog
	projection, catalog, err := BuildSafeFactProjection(canonical)
	require.NoError(t, err)
	require.NotEmpty(t, projection.Technologies)
	require.NotEmpty(t, catalog.Facts)

	// 3. Create real Gemini Provider via configuration
	model := os.Getenv("AI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	cfg := Config{
		Enabled:         true,
		Provider:        "gemini",
		Model:           model,
		MaxOutputTokens: 2048,
		Timeout:         30 * time.Second,
		Temperature:     0.0,
		GeminiAPIKey:    apiKey,
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	require.Equal(t, "gemini", provider.Name())

	// 4. Invoke SynthesizeArchitectureSummary
	taskID := "task-live-smoke-1"
	sessionID := "sess-live-smoke-1"
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	resp, valRes, err := SynthesizeArchitectureSummary(
		ctx,
		cfg,
		provider,
		canonical,
		taskID,
		sessionID,
		true,
	)

	// 5. Verification Gate
	require.NoError(t, err)
	require.NotNil(t, valRes, "Validation result must be returned")
	require.True(t, valRes.Valid, "Response must pass fail-closed validation: reasons: %v", valRes.RejectionReasons)
	require.NotNil(t, resp, "Synthesis response must not be nil when validation passes")

	// 6. Response Quality & Fact ID whitelisting
	assert.Equal(t, taskID, resp.TaskID, "TaskID must match requested task")
	assert.Equal(t, "gemini", resp.Provider, "Provider must be gemini")
	assert.NotEmpty(t, resp.Summary, "Summary must not be empty")
	assert.Empty(t, valRes.InvalidFactIDs, "No invalid Fact IDs must be cited")

	// 7. Verify Canonical Enrichment
	require.NotNil(t, canonical.AI, "CanonicalAnalysisResult.AI must be populated with validated summary")
	aiResp, ok := canonical.AI.(*SynthesisResponse)
	require.True(t, ok)
	assert.Equal(t, resp.Summary, aiResp.Summary)
}
