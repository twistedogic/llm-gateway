// Package llm provides unified request/response types and utilities
// shared across all provider implementations.
package llm

import "time"

// Message represents a chat message.
type Message struct {
	Role         string `json:"role"`
	Content      string `json:"content"`
	Name         string `json:"name,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	ToolFunction string `json:"tool_function,omitempty"`
}

// Request represents a unified LLM request.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	User        string    `json:"user,omitempty"`
	
	// Internal fields (not serialized)
	Provider string `json:"-"`
	KeyID    string `json:"-"`
}

// Response represents a unified LLM response.
type Response struct {
	ID        string    `json:"id"`
	Object    string    `json:"object,omitempty"`
	Created   int64     `json:"created,omitempty"`
	Model     string    `json:"model"`
	Choices   Message   `json:"choices"`
	Usage     Usage     `json:"usage"`
	
	// Internal fields
	Error     string    `json:"error,omitempty"`
	Provider  string    `json:"-"`
	Latency   time.Duration `json:"-"`
}

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice represents a streaming choice (used for SSE).
type Choice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

// StreamingChunk represents a streaming response chunk.
type StreamingChunk struct {
	ID      string `json:"id"`
	Choices []Choice `json:"choices"`
	Model   string `json:"model"`
}

// AddSystemMessage adds a system message to the beginning of the messages.
func (r *Request) AddSystemMessage(content string) {
	r.Messages = append([]Message{{Role: "system", Content: content}}, r.Messages...)
}

// GetLastUserMessage returns the content of the last user message.
func (r *Request) GetLastUserMessage() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].Content
		}
	}
	return ""
}

// GetSystemMessage returns the system message if present.
func (r *Request) GetSystemMessage() string {
	for _, msg := range r.Messages {
		if msg.Role == "system" {
			return msg.Content
		}
	}
	return ""
}

// UpdateSystemMessage updates the system message.
func (r *Request) UpdateSystemMessage(content string) {
	for i, msg := range r.Messages {
		if msg.Role == "system" {
			r.Messages[i].Content = content
			return
		}
	}
	// If no system message exists, add one
	r.AddSystemMessage(content)
}

// CountMessages returns the number of messages in the conversation.
func (r *Request) CountMessages() int {
	return len(r.Messages)
}

// CountToolCalls counts tool calls in the response.
func (r *Response) CountToolCalls() int {
	if r.Choices.ToolCallID == "" {
		return 0
	}
	return 1
}
