// Package tools provides function-calling tool registry and execution.
// Includes: built-in tools (time, fetch-url, read-file, exec),
// provider schema conversion, Ollama compat layer, and caching.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ToolHandler is the function signature for tool implementations.
type ToolHandler func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

// ToolDef defines a registered tool.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"parameters"` // JSON Schema
	Handler     ToolHandler     `json:"-"`
	Timeout     time.Duration   `json:"timeout"`
	CacheTTL    time.Duration   `json:"cache_ttl"` // 0 = no cache
}

// ToolCall represents a single tool call from the LLM.
type ToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Output  json.RawMessage `json:"output,omitempty"`
	Error   string          `json:"error,omitempty"`
	Cached  bool            `json:"cached,omitempty"`
	Elapsed int64           `json:"elapsed_ms"`
}

// Registry manages tool definitions and caching.
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]*ToolDef
	cache  map[string]cacheEntry
}

// cacheEntry represents a cached tool result.
type cacheEntry struct {
	value     json.RawMessage
	expiresAt time.Time
}

const defaultTimeout = 30 * time.Second

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:  make(map[string]*ToolDef),
		cache:  make(map[string]cacheEntry),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(def ToolDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if def.Name == "" {
		return fmt.Errorf("tool name is required")
	}

	if def.Handler == nil {
		return fmt.Errorf("tool handler is required")
	}

	if def.Timeout == 0 {
		def.Timeout = defaultTimeout
	}

	r.tools[def.Name] = &def
	return nil
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (*ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, ok := r.tools[name]
	return def, ok
}

// List returns all registered tools.
func (r *Registry) List() []*ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*ToolDef, 0, len(r.tools))
	for _, def := range r.tools {
		result = append(result, def)
	}
	return result
}

// getCached retrieves a cached result if available and not expired.
func (r *Registry) getCached(key string) (json.RawMessage, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.cache[key]
	if !ok {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		return nil, false
	}

	return entry.value, true
}

// setCached stores a result in the cache.
func (r *Registry) setCached(key string, value json.RawMessage, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// ClearCache removes all cached entries.
func (r *Registry) ClearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]cacheEntry)
}

// Executor runs tool calls.
type Executor struct {
	registry *Registry
	timeout  time.Duration
}

// NewExecutor creates a new tool executor.
func NewExecutor(registry *Registry, defaultTimeout time.Duration) *Executor {
	if defaultTimeout == 0 {
		defaultTimeout = 30 * time.Second
	}
	return &Executor{
		registry: registry,
		timeout:  defaultTimeout,
	}
}

// ExecuteAll runs multiple tool calls in parallel.
func (e *Executor) ExecuteAll(ctx context.Context, calls []ToolCall) ([]ToolResult, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	results := make([]ToolResult, len(calls))
	
	// Run in parallel using goroutines
	type execResult struct {
		index  int
		result ToolResult
	}
	
	resultChan := make(chan execResult, len(calls))
	
	for i, call := range calls {
		go func(idx int, tc ToolCall) {
			res, _ := e.executeOne(ctx, tc)
			resultChan <- execResult{idx, res}
		}(i, call)
	}
	
	// Collect results
	for range calls {
		r := <-resultChan
		results[r.index] = r.result
	}
	
	return results, nil
}

// executeOne runs a single tool call.
func (e *Executor) executeOne(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, ok := e.registry.Get(call.Function.Name)
	if !ok {
		return ToolResult{
			ID:    call.ID,
			Name:  call.Function.Name,
			Error: fmt.Sprintf("unknown tool: %s", call.Function.Name),
		}, nil
	}

	// Check cache
	cacheKey := fmt.Sprintf("%s:%s", tool.Name, call.Function.Arguments)
	if tool.CacheTTL > 0 {
		if cached, ok := e.registry.getCached(cacheKey); ok {
			return ToolResult{
				ID:     call.ID,
				Name:   tool.Name,
				Output: cached,
				Cached: true,
			}, nil
		}
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, tool.Timeout)
	defer cancel()

	start := time.Now()
	output, err := tool.Handler(execCtx, json.RawMessage(call.Function.Arguments))
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		return ToolResult{
			ID:      call.ID,
			Name:    tool.Name,
			Output:  errJSON,
			Error:   err.Error(),
			Elapsed: elapsed,
		}, nil
	}

	// Cache result
	if tool.CacheTTL > 0 && output != nil {
		e.registry.setCached(cacheKey, output, tool.CacheTTL)
	}

	return ToolResult{
		ID:      call.ID,
		Name:    tool.Name,
		Output:  output,
		Elapsed: elapsed,
	}, nil
}

// ConvertToOpenAI converts tools to OpenAI format.
func ConvertToOpenAI(tools []*ToolDef) interface{} {
	out := make([]map[string]interface{}, len(tools))
	for i, t := range tools {
		out[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Schema,
			},
		}
	}
	return out
}

// ConvertToAnthropic converts tools to Anthropic format.
func ConvertToAnthropic(tools []*ToolDef) []map[string]interface{} {
	out := make([]map[string]interface{}, len(tools))
	for i, t := range tools {
		out[i] = map[string]interface{}{
			"name":         t.Name,
			"description": t.Description,
			"input_schema": t.Schema,
		}
	}
	return out
}

// ConvertToProvider converts tools to the appropriate format for a provider.
func ConvertToProvider(tools []*ToolDef, provider string) interface{} {
	provider = strings.ToLower(provider)
	switch provider {
	case "anthropic":
		return ConvertToAnthropic(tools)
	default:
		return ConvertToOpenAI(tools)
	}
}
