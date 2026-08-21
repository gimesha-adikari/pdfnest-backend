package ai

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultProvider        = "mock"
	DefaultMaxOutputTokens = 2048
	DefaultTimeout         = 15 * time.Second
	DefaultTemperature     = float32(0.0)
)

// Config encapsulates runtime configuration for the AI architecture synthesis layer.
type Config struct {
	Enabled         bool          `json:"enabled"`
	Provider        string        `json:"provider"`
	Model           string        `json:"model"`
	MaxOutputTokens int           `json:"maxOutputTokens"`
	Timeout         time.Duration `json:"timeout"`
	Temperature     float32       `json:"temperature"`

	// Credentials (in-memory only, never serialized or logged)
	GeminiAPIKey    string `json:"-"`
	OpenAIAPIKey    string `json:"-"`
	AnthropicAPIKey string `json:"-"`
}

// LoadConfigFromEnv loads configuration from standard environment variables with safe defaults.
// AI synthesis remains disabled by default unless AI_ENABLED=true.
func LoadConfigFromEnv() Config {
	cfg := Config{
		Enabled:         false,
		Provider:        DefaultProvider,
		Model:           "",
		MaxOutputTokens: DefaultMaxOutputTokens,
		Timeout:         DefaultTimeout,
		Temperature:     DefaultTemperature,
	}

	if val := os.Getenv("AI_ENABLED"); val != "" {
		cfg.Enabled = strings.EqualFold(val, "true") || val == "1"
	}

	if val := strings.TrimSpace(os.Getenv("AI_PROVIDER")); val != "" {
		cfg.Provider = strings.ToLower(val)
	}

	if val := strings.TrimSpace(os.Getenv("AI_MODEL")); val != "" {
		cfg.Model = val
	}

	if val := os.Getenv("AI_MAX_OUTPUT_TOKENS"); val != "" {
		if tokens, err := strconv.Atoi(val); err == nil && tokens > 0 {
			cfg.MaxOutputTokens = tokens
		}
	}

	if val := os.Getenv("AI_TIMEOUT_SECONDS"); val != "" {
		if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
			cfg.Timeout = time.Duration(secs) * time.Second
		}
	}

	if val := os.Getenv("AI_TEMPERATURE"); val != "" {
		if temp, err := strconv.ParseFloat(val, 32); err == nil && temp >= 0 {
			cfg.Temperature = float32(temp)
		}
	}

	cfg.GeminiAPIKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	cfg.OpenAIAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	cfg.AnthropicAPIKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))

	return cfg
}
