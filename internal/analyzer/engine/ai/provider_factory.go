package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	MaxHTTPResponseBytes = 1024 * 1024 // 1 MB bounded response read
)

// NewProvider constructs the configured Provider instance based on runtime settings.
func NewProvider(cfg Config) (Provider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "mock", "":
		return NewMockProvider(nil, nil, 0), nil
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("%w: GEMINI_API_KEY is required", ErrProviderAuthenticationFailed)
		}
		model := cfg.Model
		if model == "" {
			model = "gemini-2.5-flash"
		}
		return NewGeminiProvider(cfg.GeminiAPIKey, model, cfg.Timeout, ""), nil
	case "openai":
		if cfg.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("%w: OPENAI_API_KEY is required", ErrProviderAuthenticationFailed)
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		return NewOpenAIProvider(cfg.OpenAIAPIKey, model, cfg.Timeout, ""), nil
	case "anthropic":
		if cfg.AnthropicAPIKey == "" {
			return nil, fmt.Errorf("%w: ANTHROPIC_API_KEY is required", ErrProviderAuthenticationFailed)
		}
		model := cfg.Model
		if model == "" {
			model = "claude-3-5-haiku-20241022"
		}
		return NewAnthropicProvider(cfg.AnthropicAPIKey, model, cfg.Timeout, ""), nil
	default:
		return nil, fmt.Errorf("unsupported AI provider '%s'", cfg.Provider)
	}
}

// -------------------------------------------------------------------------
// Gemini REST Provider Adapter
// -------------------------------------------------------------------------

type GeminiProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func NewGeminiProvider(apiKey, model string, timeout time.Duration, baseURL string) *GeminiProvider {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	return &GeminiProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *GeminiProvider) Name() string {
	return "gemini"
}

func (p *GeminiProvider) Synthesize(ctx context.Context, req SynthesisRequest) (*SynthesisResponse, error) {
	promptPayload, err := BuildPromptPayload(req.Facts, req.Catalog, DefaultMaxPromptBytes)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, p.model, p.apiKey)

	geminiReq := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{{"text": promptPayload.SystemInstruction}},
		},
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": promptPayload.UserData}}},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"temperature":      req.Temperature,
			"maxOutputTokens":  req.MaxOutputTokens,
		},
	}

	bodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	startTime := time.Now()
	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxHTTPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrProviderInvalidResponse, err)
	}

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, ErrProviderAuthenticationFailed
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrProviderRateLimited
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP status %d", ErrProviderUnavailable, httpResp.StatusCode)
	}

	var rawResponse struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(respBytes, &rawResponse); err != nil || len(rawResponse.Candidates) == 0 || len(rawResponse.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("%w: malformed gemini response", ErrProviderInvalidResponse)
	}

	responseText := strings.TrimSpace(rawResponse.Candidates[0].Content.Parts[0].Text)
	if strings.HasPrefix(responseText, "```json") {
		responseText = strings.TrimPrefix(responseText, "```json")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	} else if strings.HasPrefix(responseText, "```") {
		responseText = strings.TrimPrefix(responseText, "```")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	}

	var synthesisResp SynthesisResponse
	if err := json.Unmarshal([]byte(responseText), &synthesisResp); err != nil {
		return nil, fmt.Errorf("%w: json decode synthesis response: %v", ErrProviderInvalidResponse, err)
	}

	synthesisResp.ProtocolVersion = "1.0.0"
	synthesisResp.TaskID = req.TaskID
	synthesisResp.Provider = "gemini"
	synthesisResp.Model = p.model
	synthesisResp.DurationMs = time.Since(startTime).Milliseconds()
	synthesisResp.InputTokens = rawResponse.UsageMetadata.PromptTokenCount
	synthesisResp.OutputTokens = rawResponse.UsageMetadata.CandidatesTokenCount

	return &synthesisResp, nil
}

// -------------------------------------------------------------------------
// OpenAI REST Provider Adapter
// -------------------------------------------------------------------------

type OpenAIProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func NewOpenAIProvider(apiKey, model string, timeout time.Duration, baseURL string) *OpenAIProvider {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai"
}

func (p *OpenAIProvider) Synthesize(ctx context.Context, req SynthesisRequest) (*SynthesisResponse, error) {
	promptPayload, err := BuildPromptPayload(req.Facts, req.Catalog, DefaultMaxPromptBytes)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"

	openAIReq := map[string]interface{}{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": promptPayload.SystemInstruction},
			{"role": "user", "content": promptPayload.UserData},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     req.Temperature,
		"max_tokens":      req.MaxOutputTokens,
	}

	bodyBytes, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	startTime := time.Now()
	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxHTTPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrProviderInvalidResponse, err)
	}

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, ErrProviderAuthenticationFailed
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrProviderRateLimited
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP status %d", ErrProviderUnavailable, httpResp.StatusCode)
	}

	var rawResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBytes, &rawResponse); err != nil || len(rawResponse.Choices) == 0 {
		return nil, fmt.Errorf("%w: malformed openai response", ErrProviderInvalidResponse)
	}

	responseText := strings.TrimSpace(rawResponse.Choices[0].Message.Content)
	if strings.HasPrefix(responseText, "```json") {
		responseText = strings.TrimPrefix(responseText, "```json")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	} else if strings.HasPrefix(responseText, "```") {
		responseText = strings.TrimPrefix(responseText, "```")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	}

	var synthesisResp SynthesisResponse
	if err := json.Unmarshal([]byte(responseText), &synthesisResp); err != nil {
		return nil, fmt.Errorf("%w: json decode synthesis response: %v", ErrProviderInvalidResponse, err)
	}

	synthesisResp.ProtocolVersion = "1.0.0"
	synthesisResp.TaskID = req.TaskID
	synthesisResp.Provider = "openai"
	synthesisResp.Model = p.model
	synthesisResp.DurationMs = time.Since(startTime).Milliseconds()
	synthesisResp.InputTokens = rawResponse.Usage.PromptTokens
	synthesisResp.OutputTokens = rawResponse.Usage.CompletionTokens

	return &synthesisResp, nil
}

// -------------------------------------------------------------------------
// Anthropic REST Provider Adapter
// -------------------------------------------------------------------------

type AnthropicProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func NewAnthropicProvider(apiKey, model string, timeout time.Duration, baseURL string) *AnthropicProvider {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) Synthesize(ctx context.Context, req SynthesisRequest) (*SynthesisResponse, error) {
	promptPayload, err := BuildPromptPayload(req.Facts, req.Catalog, DefaultMaxPromptBytes)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	url := p.baseURL + "/v1/messages"

	anthropicReq := map[string]interface{}{
		"model":      p.model,
		"system":     promptPayload.SystemInstruction,
		"max_tokens": req.MaxOutputTokens,
		"messages": []map[string]string{
			{"role": "user", "content": promptPayload.UserData},
		},
	}

	bodyBytes, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	startTime := time.Now()
	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxHTTPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrProviderInvalidResponse, err)
	}

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, ErrProviderAuthenticationFailed
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrProviderRateLimited
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP status %d", ErrProviderUnavailable, httpResp.StatusCode)
	}

	var rawResponse struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBytes, &rawResponse); err != nil || len(rawResponse.Content) == 0 {
		return nil, fmt.Errorf("%w: malformed anthropic response", ErrProviderInvalidResponse)
	}

	responseText := strings.TrimSpace(rawResponse.Content[0].Text)
	if strings.HasPrefix(responseText, "```json") {
		responseText = strings.TrimPrefix(responseText, "```json")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	} else if strings.HasPrefix(responseText, "```") {
		responseText = strings.TrimPrefix(responseText, "```")
		responseText = strings.TrimSuffix(responseText, "```")
		responseText = strings.TrimSpace(responseText)
	}

	var synthesisResp SynthesisResponse
	if err := json.Unmarshal([]byte(responseText), &synthesisResp); err != nil {
		return nil, fmt.Errorf("%w: json decode synthesis response: %v", ErrProviderInvalidResponse, err)
	}

	synthesisResp.ProtocolVersion = "1.0.0"
	synthesisResp.TaskID = req.TaskID
	synthesisResp.Provider = "anthropic"
	synthesisResp.Model = p.model
	synthesisResp.DurationMs = time.Since(startTime).Milliseconds()
	synthesisResp.InputTokens = rawResponse.Usage.InputTokens
	synthesisResp.OutputTokens = rawResponse.Usage.OutputTokens

	return &synthesisResp, nil
}
