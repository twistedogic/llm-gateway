// Package context provides context compaction functionality.
// When conversation history exceeds a threshold, older messages are
// summarized via async Temporal workflow.
package context

import (
	"context"
	"strings"

	"github.com/example/llm-gateway/internal/llm"
)

// Config holds compaction configuration.
type Config struct {
	Enabled       bool
	Threshold     float64 // 0.0 - 1.0, trigger at % of context window
	Strategy      string  // "summarize", "truncate", "hybrid"
	SummaryPrompt string
	Async         bool
	Model         string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:   true,
		Threshold: 0.85,
		Strategy:  "summarize",
		SummaryPrompt: "Summarize the following conversation concisely, preserving:\n" +
			"- All factual information, numbers, and names mentioned\n" +
			"- Key decisions and conclusions reached\n" +
			"- User goals and constraints\n" +
			"- Any unfinished tasks or follow-up items",
		Async: true,
	}
}

// Compactor handles context compaction.
type Compactor struct {
	cfg       Config
	tokenizer TokenCounter
	provider  Summarizer
}

// TokenCounter estimates token count.
type TokenCounter interface {
	Count(text string) int
	CountMessages(messages []llm.Message) int
}

// SimpleTokenizer provides a rough token estimate.
type SimpleTokenizer struct{}

// Count estimates tokens as ~4 characters per token.
func (t *SimpleTokenizer) Count(text string) int {
	return len(text) / 4
}

// CountMessages estimates total tokens in messages.
func (t *SimpleTokenizer) CountMessages(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		// Add overhead for role
		total += t.Count(msg.Content) + 4
	}
	return total
}

// Summarizer handles message summarization.
type Summarizer interface {
	Summarize(ctx context.Context, messages []llm.Message, prompt string) (string, error)
}

// NewCompactor creates a new context compactor.
func NewCompactor(cfg Config, tokenizer TokenCounter) *Compactor {
	if tokenizer == nil {
		tokenizer = &SimpleTokenizer{}
	}
	return &Compactor{
		cfg:       cfg,
		tokenizer: tokenizer,
	}
}

// SetSummarizer sets the summarization provider.
func (c *Compactor) SetSummarizer(provider Summarizer) {
	c.provider = provider
}

// ShouldCompact checks if compaction is needed.
func (c *Compactor) ShouldCompact(req *llm.Request, contextLimit int) bool {
	if !c.cfg.Enabled {
		return false
	}

	tokenCount := c.tokenizer.CountMessages(req.Messages)
	threshold := float64(contextLimit) * c.cfg.Threshold

	return float64(tokenCount) >= threshold
}

// CompactResult holds the result of compaction.
type CompactResult struct {
	Request    *llm.Request
	Compacted  bool
	Err        error
}

// Compact reduces the conversation history.
func (c *Compactor) Compact(ctx context.Context, req *llm.Request) (*llm.Request, bool, error) {
	if !c.ShouldCompact(req, 128000) { // Default 128k context
		return req, false, nil
	}

	switch c.cfg.Strategy {
	case "truncate":
		return c.truncate(req)
	case "summarize":
		return c.summarize(ctx, req)
	case "hybrid":
		return c.hybrid(ctx, req)
	default:
		return c.truncate(req)
	}
}

// truncate drops oldest messages until under threshold.
func (c *Compactor) truncate(req *llm.Request) (*llm.Request, bool, error) {
	// Keep system message + last N messages
	minMessages := 2 // system + last user
	maxMessages := 10

	// Remove from the middle (keep recent messages)
	if len(req.Messages) > maxMessages {
		// Find the last user message to keep as starting point
		keepIndex := len(req.Messages) - maxMessages
		if keepIndex < minMessages {
			keepIndex = minMessages
		}

		// Check if there's a system message
		hasSystem := len(req.Messages) > 0 && req.Messages[0].Role == "system"

		var newMessages []llm.Message
		if hasSystem {
			newMessages = append(newMessages, req.Messages[0]) // Keep system
			newMessages = append(newMessages, llm.Message{
				Role:    "system",
				Content: "[Previous conversation truncated due to length]",
			})
			newMessages = append(newMessages, req.Messages[keepIndex:]...)
		} else {
			newMessages = append(newMessages, llm.Message{
				Role:    "system",
				Content: "[Previous conversation truncated due to length]",
			})
			newMessages = append(newMessages, req.Messages[keepIndex:]...)
		}

		req.Messages = newMessages
	}

	return req, true, nil
}

// summarize replaces older messages with a summary.
func (c *Compactor) summarize(ctx context.Context, req *llm.Request) (*llm.Request, bool, error) {
	if c.provider == nil {
		// Fall back to truncate if no summarizer
		return c.truncate(req)
	}

	// Split messages: recent (keep) and older (summarize)
	keepCount := len(req.Messages) / 2
	if keepCount < 3 {
		keepCount = 3
	}

	olderMessages := req.Messages[:keepCount]
	recentMessages := req.Messages[keepCount:]

	// Build summary prompt
	var prompt strings.Builder
	prompt.WriteString(c.cfg.SummaryPrompt)
	prompt.WriteString("\n\nMessages to summarize:\n")
	for _, msg := range olderMessages {
		prompt.WriteString(msg.Role + ": " + msg.Content + "\n")
	}

	// Get summary
	summary, err := c.provider.Summarize(ctx, olderMessages, prompt.String())
	if err != nil {
		// Fall back to truncate on error
		return c.truncate(req)
	}

	// Build new messages
	var newMessages []llm.Message

	// Keep system message
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		newMessages = append(newMessages, req.Messages[0])
	}

	// Add summary
	newMessages = append(newMessages, llm.Message{
		Role:    "system",
		Content: "[Previous conversation summarized]: " + summary,
	})

	// Add recent messages
	newMessages = append(newMessages, recentMessages...)

	req.Messages = newMessages
	return req, true, nil
}

// hybrid uses summarize if very long, otherwise truncate.
func (c *Compactor) hybrid(ctx context.Context, req *llm.Request) (*llm.Request, bool, error) {
	tokenCount := c.tokenizer.CountMessages(req.Messages)
	contextLimit := 128000

	// If over 150% threshold, just truncate
	if float64(tokenCount) >= float64(contextLimit)*1.5 {
		return c.truncate(req)
	}

	// Otherwise, summarize
	return c.summarize(ctx, req)
}

// GetTopicHints extracts topic hints from request headers.
func GetTopicHints(ctx context.Context, headerName string) []string {
	// This would read from request headers
	// Implementation depends on HTTP context
	return nil
}
