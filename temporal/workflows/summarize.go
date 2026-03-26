// Package workflows contains Temporal workflow definitions for llm-gateway.
package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/example/llm-gateway/temporal/activities"
)

// SummarizeInput contains the input for conversation summarization.
type SummarizeInput struct {
	ConversationID string
	Messages       []Message
	TargetModel    string
}

// Message represents a chat message in workflows.
type Message struct {
	Role    string
	Content string
}

// SummarizeResult contains the result of summarization.
type SummarizeResult struct {
	Summary   string
	OriginalCount int
	Summarized bool
}

// ConversationSummarizeWorkflow summarizes older conversation messages
// and replaces them with a summary. This is an async operation triggered
// when context exceeds a threshold.
func ConversationSummarizeWorkflow(ctx workflow.Context, input SummarizeInput) (*SummarizeResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &workflow.RetryPolicy{
			MaximumAttempts: 3,
			InitialInterval: 1 * time.Second,
			BackoffCoefficient: 2.0,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	logger := workflow.GetLogger(ctx)
	logger.Info("Starting conversation summarization",
		"conversation_id", input.ConversationID,
		"message_count", len(input.Messages))

	// Step 1: Summarize the older half of the conversation
	var summary string
	err := workflow.ExecuteActivity(ctx, activities.SummarizeActivity, activities.SummarizeInput{
		Messages:    input.Messages,
		TargetModel: input.TargetModel,
	}).Get(ctx, &summary)

	if err != nil {
		logger.Error("Summarization activity failed", "error", err)
		return &SummarizeResult{
			OriginalCount: len(input.Messages),
			Summarized:    false,
		}, err
	}

	logger.Info("Conversation summarized successfully",
		"original_count", len(input.Messages),
		"summary_length", len(summary))

	// Step 2: Update the conversation (replace older messages with summary)
	var updated bool
	err = workflow.ExecuteActivity(ctx, activities.UpdateConversationActivity, activities.UpdateInput{
		ConversationID: input.ConversationID,
		OriginalCount:  len(input.Messages),
		SummaryMessage: summary,
	}).Get(ctx, &updated)

	if err != nil {
		logger.Error("Update conversation activity failed", "error", err)
		// Don't fail the workflow - summarization succeeded
	}

	return &SummarizeResult{
		Summary:        summary,
		OriginalCount:  len(input.Messages),
		Summarized:     true,
	}, nil
}

// AuditHookWorkflow handles audit logging via Temporal for durability.
type AuditHookInput struct {
	RequestID   string
	KeyID       string
	Provider    string
	Model       string
	TokensUsed  int
	LatencyMs   int64
	Timestamp   time.Time
	Success     bool
	ErrorMessage string
}

// AuditHookResult contains the result of audit logging.
type AuditHookResult struct {
	AuditID string
	Stored  bool
}

// AuditHookWorkflow logs audit events durably via Temporal.
func AuditHookWorkflow(ctx workflow.Context, input AuditHookInput) (*AuditHookResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &workflow.RetryPolicy{
			MaximumAttempts: 5,
			InitialInterval: 500 * time.Millisecond,
			BackoffCoefficient: 2.0,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	logger := workflow.GetLogger(ctx)
	logger.Info("Processing audit event",
		"request_id", input.RequestID,
		"key_id", input.KeyID)

	var auditID string
	err := workflow.ExecuteActivity(ctx, activities.StoreAuditActivity, activities.AuditRecord{
		RequestID:    input.RequestID,
		KeyID:        input.KeyID,
		Provider:     input.Provider,
		Model:        input.Model,
		TokensUsed:   input.TokensUsed,
		LatencyMs:    input.LatencyMs,
		Timestamp:    input.Timestamp,
		Success:      input.Success,
		ErrorMessage: input.ErrorMessage,
	}).Get(ctx, &auditID)

	if err != nil {
		logger.Error("Failed to store audit record", "error", err)
		return &AuditHookResult{Stored: false}, err
	}

	return &AuditHookResult{
		AuditID: auditID,
		Stored:  true,
	}, nil
}

// CompactContextWorkflow handles context compaction when a conversation
// exceeds the context window threshold.
type CompactContextInput struct {
	ConversationID   string
	CurrentMessages  []Message
	ContextLimit     int
	ThresholdPercent float64
	Strategy         string // "truncate", "summarize", "hybrid"
}

// CompactContextWorkflow compacts conversation context.
func CompactContextWorkflow(ctx workflow.Context, input CompactContextInput) (*SummarizeResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &workflow.RetryPolicy{
			MaximumAttempts: 3,
			InitialInterval: 2 * time.Second,
			BackoffCoefficient: 2.0,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	logger := workflow.GetLogger(ctx)
	logger.Info("Starting context compaction",
		"conversation_id", input.ConversationID,
		"message_count", len(input.CurrentMessages),
		"strategy", input.Strategy)

	switch input.Strategy {
	case "truncate":
		return truncateMessages(ctx, input)
	case "summarize":
		return summarizeMessages(ctx, input)
	case "hybrid":
		return hybridCompact(ctx, input)
	default:
		return truncateMessages(ctx, input)
	}
}

func truncateMessages(ctx workflow.Context, input CompactContextInput) (*SummarizeResult, error) {
	threshold := int(float64(input.ContextLimit) * 0.5) // Keep last 50%
	
	if len(input.CurrentMessages) <= threshold {
		return &SummarizeResult{
			OriginalCount: len(input.CurrentMessages),
			Summarized:    false,
		}, nil
	}

	// Keep the last N messages
	keptMessages := input.CurrentMessages[len(input.CurrentMessages)-threshold:]

	var updated bool
	workflow.ExecuteActivity(ctx, activities.UpdateConversationActivity, activities.UpdateInput{
		ConversationID: input.ConversationID,
		Messages:       keptMessages,
	}).Get(ctx, &updated)

	return &SummarizeResult{
		OriginalCount: len(input.CurrentMessages),
		Summarized:    true,
	}, nil
}

func summarizeMessages(ctx workflow.Context, input CompactContextInput) (*SummarizeResult, error) {
	return ConversationSummarizeWorkflow(ctx, SummarizeInput{
		ConversationID: input.ConversationID,
		Messages:       input.CurrentMessages,
		TargetModel:    "gpt-4o-mini", // Use cheap model for summarization
	})
}

func hybridCompact(ctx workflow.Context, input CompactContextInput) (*SummarizeResult, error) {
	// If over 150% threshold, truncate
	// If over 85% threshold, summarize
	threshold := int(float64(input.ContextLimit) * 0.85)
	
	if len(input.CurrentMessages) > threshold*2 {
		return truncateMessages(ctx, input)
	}
	return summarizeMessages(ctx, input)
}
