// Package main is the entry point for llm-gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/viper"
)

// Config holds the gateway configuration.
type Config struct {
	Server   ServerConfig
	Telemetry TelemetryConfig
	Auth     AuthConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
}

// TelemetryConfig holds observability settings.
type TelemetryConfig struct {
	Enabled  bool
	Endpoint string
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	KeysPath string
}

func main() {
	// Parse CLI flags
	configPath := flag.String("config", "configs/gateway.yaml", "path to config file")
	port := flag.Int("port", 8080, "HTTP server port")
	host := flag.String("host", "0.0.0.0", "HTTP server host")
	flag.Parse()

	// Track if flags were explicitly set
	portFlag := flag.Lookup("port").Value.String()
	hostFlag := flag.Lookup("host").Value.String()

	// Initialize logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Load configuration
	cfg, err := loadConfig(*configPath)
	if err != nil {
		logger.Warn("failed to load config, using defaults", "error", err)
		cfg = &Config{
			Server: ServerConfig{
				Host: *host,
				Port: *port,
			},
		}
	} else {
		// CLI flags override config file (only if explicitly set)
		if portFlag != "8080" {
			cfg.Server.Port = *port
		}
		if hostFlag != "0.0.0.0" {
			cfg.Server.Host = *host
		}
	}

	logger.Info("starting gateway",
		"host", cfg.Server.Host,
		"port", cfg.Server.Port,
	)

	// Create HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/v1/models", modelsHandler)

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-quit:
		logger.Info("shutting down server...")
	case err := <-errCh:
		logger.Error("server error", "error", err)
	}

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped")
}

// loadConfig loads configuration from file using viper.
func loadConfig(path string) (*Config, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	// Environment variable overrides
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

// healthHandler returns server health status.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// modelsHandler returns OpenAI-compatible models list.
func modelsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{
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
