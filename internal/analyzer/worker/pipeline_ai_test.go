package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/ai"
)

func TestPipeline_AIIntegration_DisabledAndConsent(t *testing.T) {
	zipPath := createTestRepoZip(t)
	tempBaseDir := t.TempDir()

	// 1. AI Disabled (job.EnableAi = false)
	t.Run("AI disabled by user consent", func(t *testing.T) {
		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            "task-no-ai",
			SessionID:         "sess-no-ai",
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
			EnableAi:          false,
		}

		res, err := ExecutePipeline(context.Background(), job, tempBaseDir, nil)
		require.NoError(t, err)
		assert.NotNil(t, res)
		assert.Nil(t, res.AI, "res.AI must be nil when job.EnableAi is false")
	})

	// 2. AI Enabled in job but AI_ENABLED env var is false
	t.Run("AI enabled in job but AI_ENABLED is false", func(t *testing.T) {
		t.Setenv("AI_ENABLED", "false")
		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            "task-ai-env-disabled",
			SessionID:         "sess-ai-env-disabled",
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
			EnableAi:          true,
		}

		res, err := ExecutePipeline(context.Background(), job, tempBaseDir, nil)
		require.NoError(t, err)
		assert.NotNil(t, res)
		assert.Nil(t, res.AI, "res.AI must be nil when AI_ENABLED is false")
	})

	// 3. AI Enabled with mock provider synthesis
	t.Run("AI enabled and successful synthesis populates res.AI", func(t *testing.T) {
		t.Setenv("AI_ENABLED", "true")
		t.Setenv("AI_PROVIDER", "mock")

		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            "task-ai-mock",
			SessionID:         "sess-ai-mock",
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
			EnableAi:          true,
		}

		res, err := ExecutePipeline(context.Background(), job, tempBaseDir, nil)
		require.NoError(t, err)
		assert.NotNil(t, res)
		require.NotNil(t, res.AI, "res.AI must be populated after successful synthesis")

		summary, ok := res.AI.(*ai.SynthesisResponse)
		require.True(t, ok)
		assert.NotEmpty(t, summary.Summary)
		assert.NotEmpty(t, summary.ArchitecturePattern)
		assert.Equal(t, "1.0.0", summary.ProtocolVersion)
		assert.Equal(t, "task-ai-mock", summary.TaskID)
	})

	// 4. AI Enabled with missing key fails safely without breaking deterministic analysis
	t.Run("AI enabled with missing Gemini key fails safely without crash", func(t *testing.T) {
		t.Setenv("AI_ENABLED", "true")
		t.Setenv("AI_PROVIDER", "gemini")
		t.Setenv("GEMINI_API_KEY", "")

		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            "task-ai-missing-key",
			SessionID:         "sess-ai-missing-key",
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
			EnableAi:          true,
		}

		res, err := ExecutePipeline(context.Background(), job, tempBaseDir, nil)
		require.NoError(t, err, "Pipeline must succeed deterministically even if AI key is missing")
		assert.NotNil(t, res)
		assert.Nil(t, res.AI, "res.AI must be nil on AI failure")
		assert.Equal(t, "fullstack_app", res.Repository.Name)
		assert.NotEmpty(t, res.Technologies)
	})
}
