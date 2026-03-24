module github.com/example/llm-gateway

go 1.23

require (
	github.com/fsnotify/fsnotify          v1.8.0       // hot-reload config + keys
	github.com/prometheus/client_golang  v1.20.5       // Prometheus metrics
	github.com/dustin/go-humanize         v1.0.1        // human-readable numbers
	github.com/spf13/viper                v1.19.0       // config management

	// Temporal SDK
	go.temporal.io/sdk           v1.26.0
	go.temporal.io/sdk-client   v1.26.0

	// OpenTelemetry
	go.opentelemetry.io/otel                  v1.30.0
	go.opentelemetry.io/otel/sdk               v1.30.0
	go.opentelemetry.io/otel/trace             v1.30.0
	go.opentelemetry.io/otel/metric            v1.30.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.30.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.30.0
	go.opentelemetry.io/otel/exporters/prometheus v0.53.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.53.0

	// HTTP client
	github.com/go-logr/logr        v1.4.2
	github.com/google/generative-ai-go v0.14.0  // for Bedrock interop

	// Tokenizer
	github.com/pkoukk/tiktoken-go v0.7.0

	// Caching
	github.com/dgraph-io/ristretto v2.0.0+incompatible

	// Utilities
	golang.org/x/sync   v0.9.0
	gopkg.in/yaml.v3    v3.0.1
)
