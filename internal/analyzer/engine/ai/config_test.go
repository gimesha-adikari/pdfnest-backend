package ai

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfig_Defaults(t *testing.T) {
	// Clear any AI env vars
	os.Unsetenv("AI_ENABLED")
	os.Unsetenv("AI_PROVIDER")
	os.Unsetenv("AI_MODEL")
	os.Unsetenv("AI_MAX_OUTPUT_TOKENS")
	os.Unsetenv("AI_TIMEOUT_SECONDS")
	os.Unsetenv("AI_TEMPERATURE")

	cfg := LoadConfigFromEnv()
	assert.False(t, cfg.Enabled, "AI must be disabled by default")
	assert.Equal(t, "mock", cfg.Provider)
	assert.Equal(t, 2048, cfg.MaxOutputTokens)
	assert.Equal(t, 15*time.Second, cfg.Timeout)
	assert.Equal(t, float32(0.0), cfg.Temperature)
}

func TestConfig_CustomEnvironment(t *testing.T) {
	t.Setenv("AI_ENABLED", "true")
	t.Setenv("AI_PROVIDER", "gemini")
	t.Setenv("AI_MODEL", "gemini-1.5-pro")
	t.Setenv("AI_MAX_OUTPUT_TOKENS", "4096")
	t.Setenv("AI_TIMEOUT_SECONDS", "30")
	t.Setenv("AI_TEMPERATURE", "0.5")
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")

	cfg := LoadConfigFromEnv()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "gemini", cfg.Provider)
	assert.Equal(t, "gemini-1.5-pro", cfg.Model)
	assert.Equal(t, 4096, cfg.MaxOutputTokens)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	assert.Equal(t, float32(0.5), cfg.Temperature)
	assert.Equal(t, "test-gemini-key", cfg.GeminiAPIKey)
}
