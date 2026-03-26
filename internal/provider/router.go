// Package provider provides LLM provider implementations.
// Includes: OpenAI, Anthropic, Azure OpenAI, AWS Bedrock, Ollama, OpenRouter.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/example/llm-gateway/internal/auth"
	"github.com/example/llm-gateway/internal/llm"
)

// ProviderClient is the interface for LLM providers.
type ProviderClient interface {
	Chat(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (*llm.Response, error)
	Stream(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (<-chan llm.Choice, error)
	Name() string
}

// Router routes requests to the appropriate provider with fallback.
type Router struct {
	providers     map[string]ProviderClient
	fallbackChain []string
	defaultClient ProviderClient
	timeout       time.Duration
}

// RouterOption configures the Router.
type RouterOption func(*Router)

// WithTimeout sets the request timeout.
func WithTimeout(d time.Duration) RouterOption {
	return func(r *Router) { r.timeout = d }
}

// NewRouter creates a new provider router.
func NewRouter() *Router {
	return &Router{
		providers:     make(map[string]ProviderClient),
		fallbackChain: []string{"openai", "anthropic", "azure", "bedrock", "ollama"},
	}
}

// Register adds a provider to the router.
func (r *Router) Register(name string, client ProviderClient) {
	r.providers[name] = client
}

// SetDefault sets the default provider.
func (r *Router) SetDefault(name string) {
	if client, ok := r.providers[name]; ok {
		r.defaultClient = client
	}
}

// Route sends a request to the appropriate provider with fallback.
func (r *Router) Route(ctx context.Context, key *auth.Key, req *llm.Request) (*llm.Response, error) {
	// Build chain: key's provider first, then fallbacks
	chain := []string{key.Provider}
	for _, p := range r.fallbackChain {
		if p != key.Provider {
			chain = append(chain, p)
		}
	}

	var lastErr error
	for _, prov := range chain {
		if client, ok := r.providers[prov]; ok {
			resp, err := client.Chat(ctx, req, nil)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			// TODO: Log error, emit provider failure metric
			continue
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all providers failed; last error: %w", lastErr)
	}
	return nil, fmt.Errorf("no providers available")
}

// OpenAIProvider implements OpenAI-compatible API client.
type OpenAIProvider struct {
	baseURL   string
	apiKey    string
	model     string
	client    *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai"
}

// Chat sends a non-streaming chat request.
func (p *OpenAIProvider) Chat(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (*llm.Response, error) {
	url := fmt.Sprintf("%s/chat/completions", p.baseURL)
	
	body := map[string]interface{}{
		"model": req.Model,
		"messages": req.Messages,
	}
	
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(reqTools) > 0 {
		body["tools"] = reqTools
	}
	if req.Stream {
		body["stream"] = true
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.apiKey))

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var openAIResp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int    `json:"index"`
			Message      llm.Message `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	return &llm.Response{
		ID:      openAIResp.ID,
		Model:   openAIResp.Model,
		Choices: openAIResp.Choices[0].Message,
		Usage: llm.Usage{
			PromptTokens:     openAIResp.Usage.PromptTokens,
			CompletionTokens: openAIResp.Usage.CompletionTokens,
			TotalTokens:      openAIResp.Usage.TotalTokens,
		},
	}, nil
}

// Stream returns a channel for streaming responses.
func (p *OpenAIProvider) Stream(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (<-chan llm.Choice, error) {
	url := fmt.Sprintf("%s/chat/completions", p.baseURL)

	body := map[string]interface{}{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(reqTools) > 0 {
		body["tools"] = reqTools
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.apiKey))

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	ch := make(chan llm.Choice, 100)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		reader := NewSSEReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				event, err := reader.Read()
				if err == io.EOF {
					return
				}
				if err != nil {
					// Log error but continue
					continue
				}

				choice := parseStreamingChoice(event.Data)
				if choice.Index >= 0 { // Valid choice
					select {
					case ch <- choice:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

// SSEReader reads Server-Sent Events from a stream.
type SSEReader struct {
	reader *bufio.Reader
}

// NewSSEReader creates a new SSE reader.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReader(r)}
}

// SSEEvent represents a parsed SSE event.
type SSEEvent struct {
	Type string
	Data string
}

// Read reads the next SSE event.
func (s *SSEReader) Read() (*SSEEvent, error) {
	var event SSEEvent

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			// Empty line marks end of event
			if event.Data != "" || event.Type != "" {
				return &event, nil
			}
			continue
		}

		// Parse field
		if strings.HasPrefix(line, "data:") {
			event.Data = strings.TrimPrefix(line, "data:")
			event.Data = strings.TrimSpace(event.Data)
		} else if strings.HasPrefix(line, "event:") {
			event.Type = strings.TrimPrefix(line, "event:")
			event.Type = strings.TrimSpace(event.Type)
		}
	}
}

// parseStreamingChoice parses a streaming choice from SSE data.
func parseStreamingChoice(data string) llm.Choice {
	if data == "" || data == "[DONE]" {
		return llm.Choice{Index: -1}
	}

	var chunk struct {
		ID      string `json:"id"`
		Choices []struct {
			Index        int    `json:"index"`
			Delta        struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}

	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return llm.Choice{Index: -1}
	}

	if len(chunk.Choices) == 0 {
		return llm.Choice{Index: -1}
	}

	return llm.Choice{
		Index: chunk.Choices[0].Index,
		Delta: llm.Message{
			Role:    chunk.Choices[0].Delta.Role,
			Content: chunk.Choices[0].Delta.Content,
		},
		FinishReason: chunk.Choices[0].FinishReason,
	}
}

// AnthropicProvider implements Anthropic API client.
type AnthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com/v1",
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) Chat(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (*llm.Response, error) {
	url := fmt.Sprintf("%s/messages", p.baseURL)

	// Convert messages to Anthropic format
	messages := make([]map[string]interface{}, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]interface{}{
			"role": msg.Role,
			"content": msg.Content,
		}
	}

	body := map[string]interface{}{
		"model": p.model,
		"messages": messages,
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(reqTools) > 0 {
		body["tools"] = reqTools
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var anthropicResp struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Model  string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	content := ""
	if len(anthropicResp.Content) > 0 {
		content = anthropicResp.Content[0].Text
	}

	return &llm.Response{
		ID: anthropicResp.ID,
		Choices: llm.Message{
			Role:    "assistant",
			Content: content,
		},
		Usage: llm.Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}, nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (<-chan llm.Choice, error) {
	ch := make(chan llm.Choice, 100)
	return ch, nil // TODO: Implement streaming
}

// OllamaProvider implements Ollama API client.
type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaProvider creates a new Ollama provider.
func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaProvider{
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

func (p *OllamaProvider) Chat(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (*llm.Response, error) {
	url := fmt.Sprintf("%s/api/chat", p.baseURL)

	body := map[string]interface{}{
		"model": p.model,
		"messages": req.Messages,
		"stream": false,
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var ollamaResp struct {
		Model     string `json:"model"`
		CreatedAt string `json:"created_at"`
		Message   struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Done      bool `json:"done"`
		TotalDuration int64 `json:"total_duration"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount int `json:"eval_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &llm.Response{
		ID: fmt.Sprintf("ollama-%d", time.Now().UnixNano()),
		Choices: llm.Message{
			Role:    ollamaResp.Message.Role,
			Content: ollamaResp.Message.Content,
		},
		Usage: llm.Usage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: ollamaResp.EvalCount,
			TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		},
	}, nil
}

func (p *OllamaProvider) Stream(ctx context.Context, req *llm.Request, reqTools []json.RawMessage) (<-chan llm.Choice, error) {
	ch := make(chan llm.Choice, 100)
	return ch, nil // TODO: Implement streaming
}
