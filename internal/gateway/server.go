// Package gateway provides the HTTP gateway server and middleware chain.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/example/llm-gateway/internal/auth"
	"github.com/example/llm-gateway/internal/config"
	"github.com/example/llm-gateway/internal/ratelimit"
	"github.com/example/llm-gateway/internal/telemetry"
)

// Server is the main gateway HTTP server.
type Server struct {
	cfg      *config.Config
	logger   *slog.Logger
	keyStore *auth.KeyStore
	rlMgr    *ratelimit.Manager
	mux      *http.ServeMux
}

// NewServer creates a new gateway server.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	srv := &Server{
		cfg:      cfg,
		logger:   logger,
		keyStore: auth.NewKeyStore(cfg.Auth.KeysPath),
		rlMgr:    ratelimit.NewManager(int64(cfg.RateLimit.WindowSecs), int64(cfg.RateLimit.DefaultRPM), int64(cfg.RateLimit.DefaultTPM), int64(cfg.RateLimit.DefaultDaily)),
		mux:      http.NewServeMux(),
	}

	srv.setupRoutes()
	return srv
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// setupRoutes configures the HTTP routes with middleware.
func (s *Server) setupRoutes() {
	// Health endpoint (no auth)
	s.mux.HandleFunc("/health", s.healthHandler)

	// Metrics endpoint (no auth)
	s.mux.HandleFunc("/metrics", s.prometheusHandler)

	// OpenAI-compatible endpoints (with middleware chain)
	s.mux.HandleFunc("/v1/models", s.withLogging(s.withAuth(s.modelsHandler)))

	// Chat completions - main endpoint
	s.mux.HandleFunc("/v1/chat/completions", s.withLogging(s.withAuth(s.withRateLimit(s.chatCompletionsHandler))))
}

// Handlers

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) prometheusHandler(w http.ResponseWriter, r *http.Request) {
	// This would serve Prometheus metrics - simplified for now
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# Prometheus metrics endpoint\n"))
}

func (s *Server) modelsHandler(w http.ResponseWriter, r *http.Request) {
	_, span := telemetry.StartSpan(r.Context(), "gateway.models")
	defer span.End()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{
		"object": "list",
		"data": [
			{
				"id": "gateway-model",
				"object": "model",
				"created": 1700000000,
				"owned_by": "system"
			}
		]
	}`))
}

// Chat completions handler - main endpoint with streaming support
func (s *Server) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.StartSpan(r.Context(), "gateway.chat_completions")
	defer span.End()

	start := time.Now()

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Parse request
	var chatReq ChatCompletionRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Get key from context (set by auth middleware)
	key, ok := GetKeyFromContext(ctx)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Record request metrics
	telemetry.RecordRequest(key.Provider, chatReq.Model, key.ID, key.Tier, "200")
	defer func() {
		telemetry.RecordLatency(key.Provider, chatReq.Model, key.ID, "200", time.Since(start))
	}()

	// TODO: T3.x implementation
	// - Skills retrieval and injection
	// - Tools injection
	// - Provider routing and fallback
	// - Tool execution loop
	// - Context compaction trigger
	// - Post-hooks dispatch

	// Handle streaming requests
	if chatReq.Stream {
		s.handleStreaming(w, r, chatReq, key)
		return
	}

	s.writeChatResponse(w, chatReq, key)
}

// handleStreaming handles SSE streaming responses.
func (s *Server) handleStreaming(w http.ResponseWriter, r *http.Request, req ChatCompletionRequest, key *auth.Key) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Create cancellable context for streaming
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Handle client disconnect
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	// Stream placeholder response with context support
	s.streamPlaceholderWithContext(ctx, w, flusher, req, key)
}

// streamPlaceholderWithContext streams a placeholder response with context cancellation support.
func (s *Server) streamPlaceholderWithContext(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, req ChatCompletionRequest, key *auth.Key) {
	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	model := req.Model
	if model == "" {
		model = "gateway-model"
	}

	// Stream word by word for demo
	words := []string{"This", " is", " a", " placeholder", " streaming", " response", "."}
	for i := range words {
		select {
		case <-ctx.Done():
			return
		default:
			chunk := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":%d,"delta":{"role":"assistant","content":"%s"},"finish_reason":null}]}`,
				responseID, time.Now().Unix(), model, 0, words[i])

			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
			_ = i // suppress unused warning
		}
	}

	// Send final chunk
	final := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		responseID, time.Now().Unix(), model)
	fmt.Fprintf(w, "data: %s\n\n", final)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Record token usage
	telemetry.RecordTokens(key.Provider, model, "input", 10)
	telemetry.RecordTokens(key.Provider, model, "output", len(words))
}

// writeChatResponse returns a placeholder chat completion response.
func (s *Server) writeChatResponse(w http.ResponseWriter, req ChatCompletionRequest, key *auth.Key) {
	resp := ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "This is a placeholder response. Full LLM routing to be implemented.",
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	// Record token usage
	telemetry.RecordTokens(key.Provider, req.Model, "input", 10)
	telemetry.RecordTokens(key.Provider, req.Model, "output", 20)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// writeError writes an error response.
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "invalid_request_error",
			"code":    strconv.Itoa(status),
		},
	})
}

// Context keys for passing data through middleware
type contextKey string

const keyContextKey contextKey = "key"

// WithKeyContext adds the key to the context.
func WithKeyContext(ctx context.Context, key *auth.Key) context.Context {
	return context.WithValue(ctx, keyContextKey, key)
}

// GetKeyFromContext retrieves the key from context.
func GetKeyFromContext(ctx context.Context) (*auth.Key, bool) {
	key, ok := ctx.Value(keyContextKey).(*auth.Key)
	return key, ok
}

// Middleware functions (T2.1 - T2.4)

// T2.1: Logging middleware
func (s *Server) withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Create response wrapper for status capture
		wrapped := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		
		next.ServeHTTP(wrapped, r)
		
		// Log request
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// T2.3: Auth middleware
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, span := telemetry.StartSpan(r.Context(), "gateway.auth.validate_key")
		defer span.End()

		// Extract Authorization header
		authHeader := r.Header.Get(s.cfg.Auth.HeaderName)
		if authHeader == "" {
			// Also check X-API-Key header
			authHeader = r.Header.Get("X-API-Key")
			if authHeader == "" {
				s.writeError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}
		}

		// Extract bearer token
		rawKey := auth.ExtractBearerToken(authHeader)

		// Validate key
		key, err := s.keyStore.Get(rawKey)
		if err != nil {
			span.RecordError(err)
			s.writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Set key in context
		ctx = WithKeyContext(ctx, key)
		span.SetAttributes(
			telemetry.KeyValue{Key: "user.key.id", Value: key.ID},
			telemetry.KeyValue{Key: "user.key.tier", Value: key.Tier},
		)

		// Continue with enriched context
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// T2.4: Rate limiting middleware
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, span := telemetry.StartSpan(r.Context(), "gateway.ratelimit.check")
		defer span.End()

		// Get key from context (set by auth middleware)
		key, ok := GetKeyFromContext(r.Context())
		if !ok {
			// If no key in context, auth middleware should have rejected
			s.writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Check rate limits
		limits := ratelimit.RateLimitConfig{
			RPM:   key.Limits.RPM,
			TPM:   key.Limits.TPM,
			Daily: key.Limits.Daily,
		}

		allowed, limitType, retryAfter := s.rlMgr.CheckRPM(key.ID, limits)
		
		span.SetAttributes(
			telemetry.KeyValue{Key: "ratelimit.allowed", Value: allowed},
			telemetry.KeyValue{Key: "ratelimit.type", Value: limitType},
		)

		if !allowed {
			telemetry.RecordRateLimitReject(key.ID, limitType)
			
			// Set rate limit headers
			w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(key.Limits.RPM, 10))
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			
			s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		// Set rate limit headers
		limiter := s.rlMgr.GetLimiter(key.ID, limits)
		remaining := limiter.Limit() - limiter.Usage()
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limiter.Limit(), 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix()+limiter.WindowSecs(), 10))

		next.ServeHTTP(w, r)
	}
}

// ChatCompletionRequest represents an OpenAI-compatible chat request.
type ChatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []Message      `json:"messages"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  interface{}    `json:"tool_choice,omitempty"`
}

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// Tool represents a function tool definition.
type Tool struct {
	Type     string `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction represents a function tool.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// ChatCompletionResponse represents an OpenAI-compatible chat response.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a chat completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
