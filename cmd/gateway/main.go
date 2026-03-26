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

	"github.com/example/llm-gateway/internal/config"
	"github.com/example/llm-gateway/internal/gateway"
	"github.com/example/llm-gateway/internal/telemetry"
)

// main is the entry point for llm-gateway.
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
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Warn("failed to load config, using defaults", "error", err)
		// Create minimal config with defaults
		cfg = &config.Config{
			Server: config.ServerConfig{
				Host: *host,
				Port: *port,
			},
			Auth: config.AuthConfig{
				KeysPath: "configs/keys.json",
			},
			Telemetry: config.TelemetryConfig{
				Enabled: false, // Disabled by default, enable in config
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
		"config", *configPath,
	)

	// Initialize telemetry (Phase 2 - metrics middleware)
	ctx := context.Background()
	var telemetryShutdown func()
	if cfg.Telemetry.Enabled {
		telemetryShutdown, err = telemetry.InitWithConfig(ctx, telemetry.Config{
			Enabled:        cfg.Telemetry.Enabled,
			OTLPEndpoint:   cfg.Telemetry.OTLPEndpoint,
			PrometheusPort: cfg.Telemetry.PrometheusPort,
			ServiceName:    "llm-gateway",
			SampleRate:     cfg.Telemetry.SampleRate,
		})
		if err != nil {
			logger.Warn("failed to initialize telemetry", "error", err)
		} else {
			logger.Info("telemetry initialized",
				"otlp_endpoint", cfg.Telemetry.OTLPEndpoint,
				"prometheus_port", cfg.Telemetry.PrometheusPort,
			)
		}
	}

	// Create gateway server (Phase 2 - wiring all middleware)
	srv := gateway.NewServer(cfg, logger)

	// Create HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      srv, // Gateway server handles routing + middleware
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	// Shutdown telemetry
	if telemetryShutdown != nil {
		telemetryShutdown()
	}

	logger.Info("server stopped")
}
