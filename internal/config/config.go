// Package config loads and validates the gateway configuration from YAML.
// All values can be overridden via environment variables with GATEWAY_ prefix.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds the complete gateway configuration.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Auth      AuthConfig      `mapstructure:"auth"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
	Telemetry TelemetryConfig `mapstructure:"telemetry"`
	Hooks     HooksConfig     `mapstructure:"hooks"`
	Tools     ToolsConfig     `mapstructure:"tools"`
	Skills    SkillsConfig    `mapstructure:"skills"`
	Context   ContextConfig   `mapstructure:"context"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	KeysPath      string        `mapstructure:"keys_path"`
	HeaderName    string        `mapstructure:"header_name"`
	GraceDuration time.Duration `mapstructure:"grace_duration"`
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled     bool `mapstructure:"enabled"`
	WindowSecs  int  `mapstructure:"window_secs"`
	DefaultRPM  int  `mapstructure:"default_rpm"`
	DefaultTPM  int  `mapstructure:"default_tpm"`
	DefaultDaily int `mapstructure:"default_daily"`
}

// TelemetryConfig holds observability settings.
type TelemetryConfig struct {
	Enabled       bool          `mapstructure:"enabled"`
	OTLPEndpoint  string        `mapstructure:"otlp_endpoint"`
	PrometheusPort int          `mapstructure:"prometheus_port"`
	SampleRate    float64       `mapstructure:"sample_rate"`
}

// HooksConfig holds pre/post hook settings.
type HooksConfig struct {
	Pre  []HookConfig `mapstructure:"pre"`
	Post []HookConfig `mapstructure:"post"`
}

// HookConfig describes a single hook.
type HookConfig struct {
	Type      string        `mapstructure:"type"` // "http", "temporal", "logging"
	Name      string        `mapstructure:"name"`
	URL       string        `mapstructure:"url"`
	Timeout   time.Duration `mapstructure:"timeout"`
	FailOpen  bool          `mapstructure:"fail_open"`
	Workflow  string        `mapstructure:"workflow"`
	TaskQueue string         `mapstructure:"task_queue"`
}

// ToolsConfig holds tool registry settings.
type ToolsConfig struct {
	Enabled       bool           `mapstructure:"enabled"`
	Builtin       []string       `mapstructure:"builtin"`
	CustomPath    string         `mapstructure:"custom_path"`
	CacheEnabled  bool           `mapstructure:"cache_enabled"`
	CacheMaxSizeMB int           `mapstructure:"cache_max_size_mb"`
	MaxParallel   int           `mapstructure:"max_parallel"`
	DefaultTimeout time.Duration `mapstructure:"default_timeout"`
	FailOpen      bool           `mapstructure:"fail_open"`
}

// SkillsConfig holds skill retrieval settings.
type SkillsConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Path        string `mapstructure:"path"`
	MaxSkills   int    `mapstructure:"max_skills"`
	InjectMethod string `mapstructure:"inject_method"`
	TopicHeader  string `mapstructure:"topic_header"`
}

// ContextConfig holds context compaction settings.
type ContextConfig struct {
	Compaction CompactionConfig `mapstructure:"compaction"`
}

// CompactionConfig holds compaction-specific settings.
type CompactionConfig struct {
	Enabled      bool    `mapstructure:"enabled"`
	Threshold    float64 `mapstructure:"threshold"`
	Strategy     string  `mapstructure:"strategy"` // "summarize", "truncate", "hybrid"
	SummaryPrompt string `mapstructure:"summary_prompt"`
	Async        bool    `mapstructure:"async"`
	Model        string  `mapstructure:"model"`
}

// Load reads configuration from file with environment variable overrides.
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set config file
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	// Environment variable overrides (GATEWAY_ prefix)
	v.SetEnvPrefix("GATEWAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Apply defaults
	applyDefaults(&cfg)

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

// applyDefaults sets default values for optional fields.
func applyDefaults(cfg *Config) {
	// Server defaults
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}

	// Auth defaults
	if cfg.Auth.HeaderName == "" {
		cfg.Auth.HeaderName = "Authorization"
	}
	if cfg.Auth.GraceDuration == 0 {
		cfg.Auth.GraceDuration = 5 * time.Minute
	}

	// Rate limit defaults
	if cfg.RateLimit.WindowSecs == 0 {
		cfg.RateLimit.WindowSecs = 60
	}
	if cfg.RateLimit.DefaultRPM == 0 {
		cfg.RateLimit.DefaultRPM = 60
	}
	if cfg.RateLimit.DefaultTPM == 0 {
		cfg.RateLimit.DefaultTPM = 30000
	}
	if cfg.RateLimit.DefaultDaily == 0 {
		cfg.RateLimit.DefaultDaily = 10000
	}

	// Telemetry defaults
	if cfg.Telemetry.SampleRate == 0 {
		cfg.Telemetry.SampleRate = 1.0
	}

	// Tools defaults
	if cfg.Tools.MaxParallel == 0 {
		cfg.Tools.MaxParallel = 5
	}
	if cfg.Tools.DefaultTimeout == 0 {
		cfg.Tools.DefaultTimeout = 30 * time.Second
	}

	// Skills defaults
	if cfg.Skills.MaxSkills == 0 {
		cfg.Skills.MaxSkills = 5
	}
	if cfg.Skills.InjectMethod == "" {
		cfg.Skills.InjectMethod = "append"
	}
	if cfg.Skills.TopicHeader == "" {
		cfg.Skills.TopicHeader = "X-Gateway-Topics"
	}

	// Context compaction defaults
	if cfg.Context.Compaction.Threshold == 0 {
		cfg.Context.Compaction.Threshold = 0.85
	}
	if cfg.Context.Compaction.Strategy == "" {
		cfg.Context.Compaction.Strategy = "summarize"
	}
}

// Validate checks that required configuration fields are set.
func (c *Config) Validate() error {
	var errs []string

	if c.Auth.KeysPath == "" {
		errs = append(errs, "auth.keys_path is required")
	}

	if c.RateLimit.Enabled && c.RateLimit.WindowSecs <= 0 {
		errs = append(errs, "ratelimit.window_secs must be positive when enabled")
	}

	if c.Telemetry.Enabled && c.Telemetry.OTLPEndpoint == "" {
		errs = append(errs, "telemetry.otlp_endpoint is required when telemetry is enabled")
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errs, "; "))
	}

	return nil
}
