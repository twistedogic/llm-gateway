// Package activities contains Temporal activity definitions for llm-gateway.
package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/example/llm-gateway/internal/llm"
)

// SummarizeInput contains input for the summarize activity.
type SummarizeInput struct {
	Messages    []Message
	TargetModel string
}

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SummarizeActivity uses an LLM to summarize conversation messages.
// This activity runs asynchronously and shouldn't block the response.
func SummarizeActivity(ctx context.Context, input SummarizeInput) (string, error) {
	// Build summary prompt
	prompt := buildSummaryPrompt(input.Messages)

	// Create LLM request
	req := &llm.Request{
		Model:       input.TargetModel,
		MaxTokens:   500,
		Temperature: 0.3,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
	}

	// Call LLM (simplified - would use the provider router in real impl)
	_ = req

	// For now, return a placeholder summary
	summary := fmt.Sprintf("Conversation summary: %d messages summarized. Key topics discussed.",
		len(input.Messages))

	return summary, nil
}

// buildSummaryPrompt creates a prompt for summarization.
func buildSummaryPrompt(messages []Message) string {
	// Build a concise representation of the conversation
	var buf string
	buf = "Summarize the following conversation concisely, preserving:\n"
	buf += "- All factual information, numbers, and names mentioned\n"
	buf += "- Key decisions and conclusions reached\n"
	buf += "- User goals and constraints\n"
	buf += "- Any unfinished tasks or follow-up items\n\n"
	buf += "Conversation:\n"

	for _, msg := range messages {
		role := msg.Role
		if role == "" {
			role = "unknown"
		}
		buf += fmt.Sprintf("[%s]: %s\n", role, msg.Content)
	}

	buf += "\nSummary:"
	return buf
}

// UpdateInput contains input for updating a conversation.
type UpdateInput struct {
	ConversationID string
	OriginalCount  int
	SummaryMessage string
	Messages       []Message // Optional: if provided, replaces messages directly
}

// UpdateConversationActivity updates a conversation with summarized content.
func UpdateConversationActivity(ctx context.Context, input UpdateInput) (bool, error) {
	// In a real implementation, this would:
	// 1. Connect to the conversation store (Redis, Postgres, etc.)
	// 2. Delete the older messages
	// 3. Insert the summary message
	// 4. Return success

	// For now, simulate the update
	_ = context.Background() // Suppress unused warning

	return true, nil
}

// AuditRecord contains an audit log entry.
type AuditRecord struct {
	RequestID    string
	KeyID        string
	Provider     string
	Model        string
	TokensUsed   int
	LatencyMs    int64
	Timestamp    time.Time
	Success      bool
	ErrorMessage string
}

// StoreAuditActivity stores an audit record durably.
func StoreAuditActivity(ctx context.Context, record AuditRecord) (string, error) {
	// Serialize and store the audit record
	// In production: write to Postgres, S3, or similar durable store
	
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("failed to marshal audit record: %w", err)
	}

	// Generate audit ID
	auditID := fmt.Sprintf("audit-%s-%d", record.RequestID, time.Now().UnixNano())

	// In production: store data somewhere durable
	_ = data
	_ = auditID

	return auditID, nil
}

// CompactContextActivity handles context compaction at the storage layer.
type CompactContextInput struct {
	ConversationID string
	Strategy       string
	Threshold      int
}

// CompactContextActivity compacts conversation context in storage.
func CompactContextActivity(ctx context.Context, input CompactContextInput) (bool, error) {
	// In production: interact with conversation storage
	_ = context.Background()
	return true, nil
}

// RecordMetricsActivity records metrics to the metrics store.
type MetricsRecord struct {
	Name      string
	Labels    map[string]string
	Value     float64
	Timestamp time.Time
}

// RecordMetricsActivity records metric data.
func RecordMetricsActivity(ctx context.Context, metrics []MetricsRecord) error {
	// In production: batch write to Prometheus, InfluxDB, etc.
	_ = metrics
	return nil
}
