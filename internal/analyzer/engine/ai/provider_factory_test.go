package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderFactory_Mock(t *testing.T) {
	cfg := Config{Provider: "mock"}
	p, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.Equal(t, "mock", p.Name())
}

func TestProviderFactory_MissingKeys(t *testing.T) {
	// Gemini missing key
	_, err1 := NewProvider(Config{Provider: "gemini"})
	assert.ErrorIs(t, err1, ErrProviderAuthenticationFailed)

	// OpenAI missing key
	_, err2 := NewProvider(Config{Provider: "openai"})
	assert.ErrorIs(t, err2, ErrProviderAuthenticationFailed)

	// Anthropic missing key
	_, err3 := NewProvider(Config{Provider: "anthropic"})
	assert.ErrorIs(t, err3, ErrProviderAuthenticationFailed)

	// Unsupported provider
	_, err4 := NewProvider(Config{Provider: "unsupported"})
	assert.Error(t, err4)
}

func TestProviderFactory_GeminiDefaultsAndCustomModel(t *testing.T) {
	// 1. Default model
	cfgDefault := Config{Provider: "gemini", GeminiAPIKey: "key-1"}
	pDefault, err := NewProvider(cfgDefault)
	require.NoError(t, err)
	assert.Equal(t, "gemini", pDefault.Name())
	geminiPDefault, ok := pDefault.(*GeminiProvider)
	require.True(t, ok)
	assert.Equal(t, "gemini-2.5-flash", geminiPDefault.model)

	// 2. Custom model
	cfgCustom := Config{Provider: "gemini", GeminiAPIKey: "key-1", Model: "gemini-3.7-flash"}
	pCustom, err := NewProvider(cfgCustom)
	require.NoError(t, err)
	geminiPCustom, ok := pCustom.(*GeminiProvider)
	require.True(t, ok)
	assert.Equal(t, "gemini-3.7-flash", geminiPCustom.model)
}

func TestGeminiProvider_MockServer(t *testing.T) {
	expectedSummary := "Synthesized Gemini Summary"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent")
		assert.Equal(t, "test-gemini-key", r.URL.Query().Get("key"))

		responseJSON := map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"parts": []map[string]string{
							{
								"text": "```json\n" + fmtJSON(SynthesisResponse{
									ProtocolVersion: "1.0.0",
									TaskID:          "task-gemini",
									Summary:         expectedSummary,
								}) + "\n```",
							},
						},
					},
				},
			},
			"usageMetadata": map[string]int{
				"promptTokenCount":     150,
				"candidatesTokenCount": 80,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseJSON)
	}))
	defer ts.Close()

	provider := NewGeminiProvider("test-gemini-key", "gemini-2.5-flash", 2*time.Second, ts.URL)
	assert.Equal(t, "gemini", provider.Name())

	resp, err := provider.Synthesize(context.Background(), SynthesisRequest{
		TaskID: "task-gemini",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, expectedSummary, resp.Summary)
	assert.Equal(t, "gemini", resp.Provider)
	assert.Equal(t, "gemini-2.5-flash", resp.Model)
	assert.Equal(t, "task-gemini", resp.TaskID)
	assert.Equal(t, "1.0.0", resp.ProtocolVersion)
	assert.Equal(t, 150, resp.InputTokens)
	assert.Equal(t, 80, resp.OutputTokens)
}

func TestGeminiProvider_HTTPErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expectErr  error
	}{
		{
			name:       "401 Unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"error": {"code": 401, "message": "API key not valid"}}`,
			expectErr:  ErrProviderAuthenticationFailed,
		},
		{
			name:       "403 Forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"error": {"code": 403, "message": "Permission denied"}}`,
			expectErr:  ErrProviderAuthenticationFailed,
		},
		{
			name:       "429 Rate Limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error": {"code": 429, "message": "Resource exhausted"}}`,
			expectErr:  ErrProviderRateLimited,
		},
		{
			name:       "500 Internal Server Error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error": {"code": 500, "message": "Internal error"}}`,
			expectErr:  ErrProviderUnavailable,
		},
		{
			name:       "Malformed JSON response",
			statusCode: http.StatusOK,
			body:       `{"candidates": []}`,
			expectErr:  ErrProviderInvalidResponse,
		},
		{
			name:       "Unparseable inner JSON text",
			statusCode: http.StatusOK,
			body:       `{"candidates": [{"content": {"parts": [{"text": "this is not json"}]}}]}`,
			expectErr:  ErrProviderInvalidResponse,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			p := NewGeminiProvider("key", "gemini-2.5-flash", 2*time.Second, ts.URL)
			resp, err := p.Synthesize(context.Background(), SynthesisRequest{TaskID: "task-1"})
			assert.Error(t, err)
			assert.Nil(t, resp)
			assert.ErrorIs(t, err, tc.expectErr)
		})
	}
}

func TestOpenAIProvider_MockServer(t *testing.T) {
	expectedSummary := "Synthesized OpenAI Summary"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-openai-key", r.Header.Get("Authorization"))

		responseJSON := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": fmtJSON(SynthesisResponse{
							ProtocolVersion: "1.0.0",
							TaskID:          "task-openai",
							Summary:         expectedSummary,
						}),
					},
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     120,
				"completion_tokens": 60,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseJSON)
	}))
	defer ts.Close()

	provider := NewOpenAIProvider("test-openai-key", "gpt-4o-mini", 2*time.Second, ts.URL)
	assert.Equal(t, "openai", provider.Name())

	resp, err := provider.Synthesize(context.Background(), SynthesisRequest{
		TaskID: "task-openai",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, expectedSummary, resp.Summary)
	assert.Equal(t, "openai", resp.Provider)
}

func TestAnthropicProvider_MockServer(t *testing.T) {
	expectedSummary := "Synthesized Anthropic Summary"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "test-anthropic-key", r.Header.Get("x-api-key"))

		responseJSON := map[string]interface{}{
			"content": []map[string]string{
				{
					"type": "text",
					"text": fmtJSON(SynthesisResponse{
						ProtocolVersion: "1.0.0",
						TaskID:          "task-anthropic",
						Summary:         expectedSummary,
					}),
				},
			},
			"usage": map[string]int{
				"input_tokens":  200,
				"output_tokens": 90,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseJSON)
	}))
	defer ts.Close()

	provider := NewAnthropicProvider("test-anthropic-key", "claude-3-5-haiku", 2*time.Second, ts.URL)
	assert.Equal(t, "anthropic", provider.Name())

	resp, err := provider.Synthesize(context.Background(), SynthesisRequest{
		TaskID: "task-anthropic",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, expectedSummary, resp.Summary)
	assert.Equal(t, "anthropic", resp.Provider)
}

func fmtJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
