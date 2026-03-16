package config

import (
	"errors"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestListenAddr(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ListenAddr() != "0.0.0.0:8080" {
		t.Fatalf("expected 0.0.0.0:8080, got %s", cfg.ListenAddr())
	}

	cfg.Port = 9090
	cfg.Bind = "127.0.0.1"
	if cfg.ListenAddr() != "127.0.0.1:9090" {
		t.Fatalf("expected 127.0.0.1:9090, got %s", cfg.ListenAddr())
	}
}

func TestValidateDevMode(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error in dev mode, got %v", err)
	}
}

func validProductionConfig() *Config {
	cfg := DefaultConfig()
	cfg.Production = true
	cfg.PolicyFile = "policy.yaml"
	cfg.DatabaseURL = "postgres://localhost/rebuno"
	cfg.BearerToken = "secret"
	return cfg
}

func TestValidateProductionModeRequirements(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(*Config)
	}{
		{
			name:  "missing policy",
			tweak: func(c *Config) { c.PolicyFile = "" },
		},
		{
			name:  "missing database",
			tweak: func(c *Config) { c.DatabaseURL = "" },
		},
		{
			name:  "missing bearer token",
			tweak: func(c *Config) { c.BearerToken = "" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfig()
			tt.tweak(cfg)

			err := cfg.Validate()
			if !errors.Is(err, domain.ErrInvalidConfiguration) {
				t.Fatalf("expected ErrInvalidConfiguration, got %v", err)
			}
		})
	}
}

func TestValidateProductionModePasses(t *testing.T) {
	cfg := validProductionConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogLevel = "verbose"

	err := cfg.Validate()
	if !errors.Is(err, domain.ErrInvalidConfiguration) {
		t.Fatalf("expected ErrInvalidConfiguration, got %v", err)
	}
}

func TestValidateInvalidLogFormat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogFormat = "xml"

	err := cfg.Validate()
	if !errors.Is(err, domain.ErrInvalidConfiguration) {
		t.Fatalf("expected ErrInvalidConfiguration, got %v", err)
	}
}

func TestValidateAllLogLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg := DefaultConfig()
		cfg.LogLevel = level
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected %s to be valid, got %v", level, err)
		}
	}
}

func TestApplyEnv(t *testing.T) {
	cfg := DefaultConfig()

	t.Setenv("REBUNO_PORT", "9090")
	t.Setenv("REBUNO_BIND", "127.0.0.1")
	t.Setenv("REBUNO_PRODUCTION", "true")
	t.Setenv("REBUNO_LOG_LEVEL", "debug")
	t.Setenv("REBUNO_LOG_FORMAT", "text")
	t.Setenv("REBUNO_DB_URL", "postgres://test")
	t.Setenv("REBUNO_EXECUTION_TIMEOUT", "2h")
	t.Setenv("REBUNO_STEP_TIMEOUT", "10m")
	cfg.ApplyEnv()

	if cfg.Port != 9090 {
		t.Fatalf("expected port 9090, got %d", cfg.Port)
	}
	if cfg.Bind != "127.0.0.1" {
		t.Fatalf("expected bind 127.0.0.1, got %s", cfg.Bind)
	}
	if !cfg.Production {
		t.Fatal("expected Production=true")
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("expected debug, got %s", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("expected text, got %s", cfg.LogFormat)
	}
	if cfg.DatabaseURL != "postgres://test" {
		t.Fatalf("expected postgres://test, got %s", cfg.DatabaseURL)
	}
	if cfg.ExecutionTimeout != 2*time.Hour {
		t.Fatalf("expected 2h, got %v", cfg.ExecutionTimeout)
	}
	if cfg.StepTimeout != 10*time.Minute {
		t.Fatalf("expected 10m, got %v", cfg.StepTimeout)
	}
}

func TestApplyEnvBearerToken(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_BEARER_TOKEN", "my-secret-token")
	cfg.ApplyEnv()

	if cfg.BearerToken != "my-secret-token" {
		t.Fatalf("expected my-secret-token, got %s", cfg.BearerToken)
	}
}

func TestApplyEnvInvalidPortKeepsDefault(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_PORT", "notanumber")
	cfg.ApplyEnv()

	if cfg.Port != 8080 {
		t.Fatalf("expected default port 8080 on invalid input, got %d", cfg.Port)
	}
}

func TestApplyEnvInvalidDurationKeepsDefault(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_EXECUTION_TIMEOUT", "bogus")
	t.Setenv("REBUNO_STEP_TIMEOUT", "notaduration")
	cfg.ApplyEnv()

	if cfg.ExecutionTimeout != time.Hour {
		t.Fatalf("expected default execution timeout on invalid input, got %v", cfg.ExecutionTimeout)
	}
	if cfg.StepTimeout != 5*time.Minute {
		t.Fatalf("expected default step timeout on invalid input, got %v", cfg.StepTimeout)
	}
}

func TestApplyEnvProductionFalseValues(t *testing.T) {
	tests := []struct {
		name string
		val  string
	}{
		{"false", "false"},
		{"zero", "0"},
		{"arbitrary", "no"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Production = true
			t.Setenv("REBUNO_PRODUCTION", tt.val)
			cfg.ApplyEnv()

			if cfg.Production {
				t.Fatalf("expected Production=false for REBUNO_PRODUCTION=%q", tt.val)
			}
		})
	}
}

func TestApplyEnvAdditionalVars(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_POLICY", "mypolicy.yaml")
	t.Setenv("REBUNO_TLS_CERT", "/etc/tls/cert.pem")
	t.Setenv("REBUNO_TLS_KEY", "/etc/tls/key.pem")
	t.Setenv("REBUNO_CORS_ORIGINS", "http://localhost:3000")
	t.Setenv("REBUNO_OTEL_ENDPOINT", "otel.example.com:4317")
	t.Setenv("REBUNO_OTEL_INSECURE", "false")
	t.Setenv("REBUNO_OTEL_SAMPLE_RATE", "0.5")
	t.Setenv("REBUNO_DB_MAX_CONNS", "20")
	t.Setenv("REBUNO_DB_MIN_CONNS", "2")
	t.Setenv("REBUNO_RETENTION_PERIOD", "720h")
	t.Setenv("REBUNO_CLEANUP_INTERVAL", "30m")
	t.Setenv("REBUNO_AGENT_TIMEOUT", "1m")
	t.Setenv("REBUNO_RETRY_BASE_DELAY", "2s")
	t.Setenv("REBUNO_RETRY_MAX_DELAY", "1m")
	cfg.ApplyEnv()

	if cfg.PolicyFile != "mypolicy.yaml" {
		t.Fatalf("expected policy mypolicy.yaml, got %s", cfg.PolicyFile)
	}
	if cfg.TLSCert != "/etc/tls/cert.pem" {
		t.Fatalf("expected TLS cert path, got %s", cfg.TLSCert)
	}
	if cfg.TLSKey != "/etc/tls/key.pem" {
		t.Fatalf("expected TLS key path, got %s", cfg.TLSKey)
	}
	if cfg.CORSOrigins != "http://localhost:3000" {
		t.Fatalf("expected CORS origins, got %s", cfg.CORSOrigins)
	}
	if cfg.OTELEndpoint != "otel.example.com:4317" {
		t.Fatalf("expected OTEL endpoint, got %s", cfg.OTELEndpoint)
	}
	if cfg.OTELInsecure {
		t.Fatal("expected OTELInsecure=false")
	}
	if cfg.OTELSampleRate != 0.5 {
		t.Fatalf("expected sample rate 0.5, got %f", cfg.OTELSampleRate)
	}
	if cfg.DBMaxConns != 20 {
		t.Fatalf("expected max conns 20, got %d", cfg.DBMaxConns)
	}
	if cfg.DBMinConns != 2 {
		t.Fatalf("expected min conns 2, got %d", cfg.DBMinConns)
	}
	if cfg.RetentionPeriod != 720*time.Hour {
		t.Fatalf("expected retention 720h, got %v", cfg.RetentionPeriod)
	}
	if cfg.CleanupInterval != 30*time.Minute {
		t.Fatalf("expected cleanup interval 30m, got %v", cfg.CleanupInterval)
	}
	if cfg.AgentTimeout != time.Minute {
		t.Fatalf("expected agent timeout 1m, got %v", cfg.AgentTimeout)
	}
	if cfg.RetryBaseDelay != 2*time.Second {
		t.Fatalf("expected retry base delay 2s, got %v", cfg.RetryBaseDelay)
	}
	if cfg.RetryMaxDelay != time.Minute {
		t.Fatalf("expected retry max delay 1m, got %v", cfg.RetryMaxDelay)
	}
}

func TestApplyEnvInvalidDBConnsKeepsDefault(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_DB_MAX_CONNS", "abc")
	t.Setenv("REBUNO_DB_MIN_CONNS", "xyz")
	cfg.ApplyEnv()

	if cfg.DBMaxConns != 0 {
		t.Fatalf("expected default max conns 0, got %d", cfg.DBMaxConns)
	}
	if cfg.DBMinConns != 0 {
		t.Fatalf("expected default min conns 0, got %d", cfg.DBMinConns)
	}
}

func TestApplyEnvInvalidOTELSampleRateKeepsDefault(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("REBUNO_OTEL_SAMPLE_RATE", "notafloat")
	cfg.ApplyEnv()

	if cfg.OTELSampleRate != 0.1 {
		t.Fatalf("expected default sample rate 0.1, got %f", cfg.OTELSampleRate)
	}
}
