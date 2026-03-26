// Package telemetry provides OpenTelemetry instrumentation.
package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Config holds telemetry configuration.
type Config struct {
	Enabled        bool
	OTLPEndpoint   string
	PrometheusPort int
	ServiceName    string
	SampleRate     float64
}

// Provider holds OpenTelemetry resources.
type Provider struct{}

// Global provider instance
var globalProvider *Provider

// Init initializes OpenTelemetry with the given configuration.
func Init(serviceName, otlpEndpoint string) error {
	// For now, use noop - actual OTEL integration would require full imports
	globalProvider = &Provider{}
	return nil
}

// InitWithConfig initializes OpenTelemetry with full config.
func InitWithConfig(ctx context.Context, cfg Config) (func(), error) {
	if !cfg.Enabled {
		return func() {}, nil
	}
	// TODO: Initialize actual OTLP exporter when endpoint is provided
	globalProvider = &Provider{}
	return func() {}, nil
}

// StartSpan starts a new span (no-op for now).
func StartSpan(ctx context.Context, name string) (context.Context, Span) {
	return ctx, &noopSpan{}
}

// Span is the interface for tracing spans.
type Span interface {
	End()
	RecordError(err error)
	SetAttributes(attrs ...KeyValue)
}

// KeyValue represents a key-value attribute pair.
type KeyValue struct {
	Key   string
	Value interface{}
}

type noopSpan struct{}

func (s *noopSpan) End()                          {}
func (s *noopSpan) RecordError(err error)         {}
func (s *noopSpan) SetAttributes(kv ...KeyValue)   {}

// RecordError records an error on a span.
func RecordError(span interface{}, err error) {
	if s, ok := span.(*noopSpan); ok {
		s.RecordError(err)
	}
}

// Metrics instruments
var (
	mu                      sync.Mutex
	requestCounter          int64
	tokenCounter            int64
	rateLimitRejectCounter  int64
	hookInvocationCounter   int64
	requestLatencies        []float64
)

// RecordRequest records a request metric.
func RecordRequest(provider, model, keyID, tier, statusCode string) {
	mu.Lock()
	requestCounter++
	mu.Unlock()
}

// RecordLatency records request latency.
func RecordLatency(provider, model, keyID, statusCode string, duration time.Duration) {
	mu.Lock()
	requestLatencies = append(requestLatencies, duration.Seconds())
	if len(requestLatencies) > 1000 {
		requestLatencies = requestLatencies[len(requestLatencies)-1000:]
	}
	mu.Unlock()
}

// RecordTokens records token usage.
func RecordTokens(provider, model, tokenType string, count int) {
	mu.Lock()
	tokenCounter += int64(count)
	mu.Unlock()
}

// RecordRateLimitReject records a rate limit rejection.
func RecordRateLimitReject(keyID, limitType string) {
	mu.Lock()
	rateLimitRejectCounter++
	mu.Unlock()
}

// RecordHookInvocation records a hook invocation.
func RecordHookInvocation(hookName, hookType, status string) {
	mu.Lock()
	hookInvocationCounter++
	mu.Unlock()
}

// GetMetrics returns current metrics for Prometheus exposition.
func GetMetrics() string {
	mu.Lock()
	defer mu.Unlock()

	return fmt.Sprintf(`# HELP llm_gateway_requests_total Total number of requests
# TYPE llm_gateway_requests_total counter
llm_gateway_requests_total %d
# HELP llm_gateway_tokens_total Total number of tokens
# TYPE llm_gateway_tokens_total counter
llm_gateway_tokens_total %d
# HELP llm_gateway_ratelimit_rejected_total Total rate limit rejections
# TYPE llm_gateway_ratelimit_rejected_total counter
llm_gateway_ratelimit_rejected_total %d
# HELP llm_gateway_hook_invocations_total Total hook invocations
# TYPE llm_gateway_hook_invocations_total counter
llm_gateway_hook_invocations_total %d
`, requestCounter, tokenCounter, rateLimitRejectCounter, hookInvocationCounter)
}
