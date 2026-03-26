package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/example/llm-gateway/internal/llm"
)

func TestOpenAIProvider_Chat(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("Authorization") == "" {
			t.Error("expected Authorization header")
		}

		// Return mock response
		resp := `{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "gpt-4",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 5,
				"total_tokens": 15
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, "gpt-4")

	resp, err := provider.Chat(context.Background(), &llm.Request{
		Model: "gpt-4",
		Messages: []llm.Message{
			{Role: "user", Content: "Hi"},
		},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Choices.Content != "Hello!" {
		t.Errorf("expected 'Hello!', got '%s'", resp.Choices.Content)
	}

	if resp.Usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestOpenAIProvider_Chat_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("bad-key", server.URL, "gpt-4")

	_, err := provider.Chat(context.Background(), &llm.Request{
		Model:    "gpt-4",
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	}, nil)

	if err == nil {
		t.Error("expected error for invalid API key")
	}
}

func TestOllamaProvider_Chat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected application/json content type")
		}

		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		if req["model"] != "llama2" {
			t.Errorf("expected model 'llama2', got '%v'", req["model"])
		}

		resp := `{
			"model": "llama2",
			"message": {"role": "assistant", "content": "Hello from Ollama!"},
			"done": true,
			"prompt_eval_count": 10,
			"eval_count": 5
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "llama2")

	resp, err := provider.Chat(context.Background(), &llm.Request{
		Model:    "llama2",
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Choices.Content != "Hello from Ollama!" {
		t.Errorf("expected 'Hello from Ollama!', got '%s'", resp.Choices.Content)
	}
}

func TestRouter_Register(t *testing.T) {
	router := NewRouter()

	provider := NewOpenAIProvider("key", "", "gpt-4")
	router.Register("openai", provider)

	if router.providers["openai"] == nil {
		t.Error("expected openai provider to be registered")
	}
}

func TestRouter_SetDefault(t *testing.T) {
	router := NewRouter()

	provider := NewOpenAIProvider("key", "", "gpt-4")
	router.Register("openai", provider)
	router.SetDefault("openai")

	if router.defaultClient == nil {
		t.Error("expected default client to be set")
	}
}

// failingProvider always fails.
type failingProvider struct {
	name string
}

func (p *failingProvider) Chat(ctx context.Context, req *llm.Request, tools []json.RawMessage) (*llm.Response, error) {
	return nil, io.EOF
}

func (p *failingProvider) Stream(ctx context.Context, req *llm.Request, tools []json.RawMessage) (<-chan llm.Choice, error) {
	ch := make(chan llm.Choice)
	close(ch)
	return ch, nil
}

func (p *failingProvider) Name() string {
	return p.name
}

// mockProvider always succeeds.
type mockProvider struct {
	content string
}

func (p *mockProvider) Chat(ctx context.Context, req *llm.Request, tools []json.RawMessage) (*llm.Response, error) {
	return &llm.Response{
		ID: "test-id",
		Choices: llm.Message{
			Role:    "assistant",
			Content: p.content,
		},
	}, nil
}

func (p *mockProvider) Stream(ctx context.Context, req *llm.Request, tools []json.RawMessage) (<-chan llm.Choice, error) {
	ch := make(chan llm.Choice)
	close(ch)
	return ch, nil
}

func (p *mockProvider) Name() string {
	return "mock"
}
