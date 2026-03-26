package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistry_Register(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(ToolDef{
		Name:        "test-tool",
		Description: "A test tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return []byte(`{"result":"ok"}`), nil
		},
		Timeout: 1 * time.Second,
	})

	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	// Re-registration overwrites existing tool (no error)
	err = registry.Register(ToolDef{
		Name:        "test-tool",
		Description: "Updated description",
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error for re-registration: %v", err)
	}

	// Verify the tool was updated
	tool, _ := registry.Get("test-tool")
	if tool.Description != "Updated description" {
		t.Errorf("expected description 'Updated description', got '%s'", tool.Description)
	}
}

func TestRegistry_Get(t *testing.T) {
	registry := NewRegistry()

	registry.Register(ToolDef{
		Name:        "get-tool",
		Description: "Get a tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, nil
		},
	})

	tool, ok := registry.Get("get-tool")
	if !ok {
		t.Fatal("expected to find registered tool")
	}
	if tool.Name != "get-tool" {
		t.Errorf("expected name 'get-tool', got '%s'", tool.Name)
	}

	_, ok = registry.Get("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent tool")
	}
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	registry.Register(ToolDef{
		Name:    "tool1",
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil },
	})
	registry.Register(ToolDef{
		Name:    "tool2",
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil },
	})

	tools := registry.List()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestExecutor_ExecuteAll_Success(t *testing.T) {
	registry := NewRegistry()

	registry.Register(ToolDef{
		Name:        "add",
		Description: "Adds two numbers",
		Schema:      json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			var argsMap map[string]float64
			if err := json.Unmarshal(args, &argsMap); err != nil {
				return nil, err
			}
			result := map[string]float64{"sum": argsMap["a"] + argsMap["b"]}
			return json.Marshal(result)
		},
		Timeout: 1 * time.Second,
	})

	exec := NewExecutor(registry, 5*time.Second)

	calls := []ToolCall{
		{ID: "call1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "add", Arguments: `{"a":1,"b":2}`}},
	}

	results, err := exec.ExecuteAll(context.Background(), calls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("expected no error, got '%s'", results[0].Error)
	}

	var result map[string]float64
	json.Unmarshal(results[0].Output, &result)
	if result["sum"] != 3 {
		t.Errorf("expected sum 3, got %f", result["sum"])
	}
}

func TestExecutor_ExecuteAll_Parallel(t *testing.T) {
	registry := NewRegistry()
	var order []int
	var mu sync.Mutex

	for i := 0; i < 3; i++ {
		i := i // capture
		registry.Register(ToolDef{
			Name:        "parallel-" + string(rune('a'+i)),
			Description: "Parallel tool",
			Schema:      json.RawMessage(`{"type":"object"}`),
			Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return []byte(`{"done":true}`), nil
			},
			Timeout: 1 * time.Second,
		})
	}

	exec := NewExecutor(registry, 5*time.Second)

	calls := []ToolCall{
		{ID: "p1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "parallel-a"}},
		{ID: "p2", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "parallel-b"}},
		{ID: "p3", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "parallel-c"}},
	}

	start := time.Now()
	results, _ := exec.ExecuteAll(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Should take ~50ms (parallel) not ~150ms (sequential)
	if elapsed > 100*time.Millisecond {
		t.Errorf("expected parallel execution (<100ms), took %v", elapsed)
	}
}

func TestExecutor_ExecuteAll_ToolError(t *testing.T) {
	registry := NewRegistry()

	registry.Register(ToolDef{
		Name:        "error-tool",
		Description: "Returns an error",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("tool execution failed")
		},
		Timeout: 1 * time.Second,
	})

	exec := NewExecutor(registry, 5*time.Second)

	calls := []ToolCall{
		{ID: "err1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "error-tool"}},
	}

	results, err := exec.ExecuteAll(context.Background(), calls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == "" {
		t.Error("expected error in result")
	}
}

func TestExecutor_ExecuteAll_UnknownTool(t *testing.T) {
	registry := NewRegistry()
	exec := NewExecutor(registry, 5*time.Second)

	calls := []ToolCall{
		{ID: "unknown1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "does-not-exist"}},
	}

	results, _ := exec.ExecuteAll(context.Background(), calls)

	if results[0].Error == "" {
		t.Error("expected error for unknown tool")
	}
}

func TestExecutor_ExecuteAll_ContextCancellation(t *testing.T) {
	registry := NewRegistry()

	// Create a context that will be cancelled immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	registry.Register(ToolDef{
		Name:        "slow-tool",
		Description: "Slow tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			// This will block forever if context isn't cancelled
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Timeout: 2 * time.Second,
	})

	exec := NewExecutor(registry, 100*time.Millisecond)

	calls := []ToolCall{
		{ID: "slow1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "slow-tool"}},
	}

	results, _ := exec.ExecuteAll(ctx, calls)

	// Context cancellation should result in an error
	if results[0].Error == "" {
		t.Error("expected error due to cancelled context")
	}
}
