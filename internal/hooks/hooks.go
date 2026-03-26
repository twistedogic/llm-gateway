// Package hooks provides pre and post request hooks.
// Supports HTTP callbacks and Temporal workflow dispatch.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/example/llm-gateway/internal/llm"
	"github.com/example/llm-gateway/internal/telemetry"
)

// PreHook is called before the LLM request is made.
type PreHook interface {
	GetName() string
	Execute(ctx context.Context, req *llm.Request) (*llm.Request, error)
}

// PostHook is called after the LLM response is received.
type PostHook interface {
	GetName() string
	ExecutePost(ctx context.Context, req *llm.Request, resp *llm.Response) error
}

// HTTPHook calls an HTTP endpoint.
type HTTPHook struct {
	HookName string
	URL      string
	Timeout  time.Duration
	FailOpen bool
	client   *http.Client
}

// NewHTTPHook creates a new HTTP hook.
func NewHTTPHook(name, url string, timeout time.Duration, failOpen bool) *HTTPHook {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &HTTPHook{
		HookName: name,
		URL:      url,
		Timeout:  timeout,
		FailOpen: failOpen,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (h *HTTPHook) GetName() string {
	return h.HookName
}

// Execute calls the HTTP endpoint with the request data.
func (h *HTTPHook) Execute(ctx context.Context, req *llm.Request) (*llm.Request, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return req, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return req, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(httpReq)
	if err != nil {
		if h.FailOpen {
			return req, nil
		}
		return nil, fmt.Errorf("HTTP hook failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		if h.FailOpen {
			return req, nil
		}
		return nil, fmt.Errorf("HTTP hook returned status %d", resp.StatusCode)
	}

	// Try to parse response as modified request
	var modifiedReq llm.Request
	if err := json.NewDecoder(resp.Body).Decode(&modifiedReq); err == nil {
		return &modifiedReq, nil
	}

	return req, nil
}

// ExecutePost implements PostHook for HTTP.
func (h *HTTPHook) ExecutePost(ctx context.Context, req *llm.Request, resp *llm.Response) error {
	body, err := json.Marshal(map[string]interface{}{
		"request":  req,
		"response": resp,
	})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := h.client.Do(httpReq)
	if err != nil {
		if h.FailOpen {
			return nil
		}
		return fmt.Errorf("HTTP hook failed: %w", err)
	}
	defer httpResp.Body.Close()
	io.Copy(io.Discard, httpResp.Body)

	if httpResp.StatusCode >= 400 && !h.FailOpen {
		return fmt.Errorf("HTTP hook returned status %d", httpResp.StatusCode)
	}

	return nil
}

// LoggingHook logs requests and responses.
type LoggingHook struct {
	HookName string
}

func (h *LoggingHook) GetName() string {
	return h.HookName
}

func (h *LoggingHook) Execute(ctx context.Context, req *llm.Request) (*llm.Request, error) {
	telemetry.RecordHookInvocation(h.GetName(), "pre", "success")
	return req, nil
}

func (h *LoggingHook) ExecutePost(ctx context.Context, req *llm.Request, resp *llm.Response) error {
	telemetry.RecordHookInvocation(h.GetName(), "post", "success")
	return nil
}

// MetricsHook records metrics for requests and responses.
type MetricsHook struct {
	HookName string
}

func (h *MetricsHook) GetName() string {
	return h.HookName
}

func (h *MetricsHook) Execute(ctx context.Context, req *llm.Request) (*llm.Request, error) {
	return req, nil
}

func (h *MetricsHook) ExecutePost(ctx context.Context, req *llm.Request, resp *llm.Response) error {
	if resp.Usage.TotalTokens > 0 {
		telemetry.RecordTokens(resp.Provider, req.Model, "input", resp.Usage.PromptTokens)
		telemetry.RecordTokens(resp.Provider, req.Model, "output", resp.Usage.CompletionTokens)
	}
	return nil
}

// HookExecutor runs hooks and manage pre/post hook execution.
type HookExecutor struct {
	preHooks  []PreHook
	postHooks []PostHook
}

// NewHookExecutor creates a new hook executor.
func NewHookExecutor() *HookExecutor {
	return &HookExecutor{
		preHooks:  make([]PreHook, 0),
		postHooks: make([]PostHook, 0),
	}
}

// AddPre adds a pre-hook.
func (e *HookExecutor) AddPre(hook PreHook) {
	e.preHooks = append(e.preHooks, hook)
}

// AddPost adds a post-hook.
func (e *HookExecutor) AddPost(hook PostHook) {
	e.postHooks = append(e.postHooks, hook)
}

// ExecutePre runs all pre-hooks in parallel.
func (e *HookExecutor) ExecutePre(ctx context.Context, req *llm.Request) (*llm.Request, error) {
	if len(e.preHooks) == 0 {
		return req, nil
	}

	result := req
	for _, hook := range e.preHooks {
		modified, err := hook.Execute(ctx, result)
		if err != nil {
			telemetry.RecordHookInvocation(hook.GetName(), "pre", "error")
			// Continue with other hooks
			continue
		}
		if modified != nil {
			result = modified
		}
	}

	return result, nil
}

// ExecutePost runs all post-hooks asynchronously (fire-and-forget).
func (e *HookExecutor) ExecutePost(ctx context.Context, req *llm.Request, resp *llm.Response) {
	for _, hook := range e.postHooks {
		go func(h PostHook) {
			if err := h.ExecutePost(ctx, req, resp); err != nil {
				telemetry.RecordHookInvocation(h.GetName(), "post", "error")
			}
		}(hook)
	}
}

// ExecutePostSync runs all post-hooks synchronously.
func (e *HookExecutor) ExecutePostSync(ctx context.Context, req *llm.Request, resp *llm.Response) {
	for _, hook := range e.postHooks {
		if err := hook.ExecutePost(ctx, req, resp); err != nil {
			telemetry.RecordHookInvocation(hook.GetName(), "post", "error")
		}
	}
}
