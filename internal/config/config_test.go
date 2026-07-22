package config

import (
	"testing"
	"time"
)

func TestDefaultHasLoggingDefaults(t *testing.T) {
	c := Default()
	if c.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", c.LogLevel)
	}
	if c.LogFormat != "text" {
		t.Fatalf("LogFormat = %q, want text", c.LogFormat)
	}
	if c.OTELSampleRate != 1.0 {
		t.Fatalf("OTELSampleRate = %v, want 1.0", c.OTELSampleRate)
	}
}

func TestFromEnvReadsNewFields(t *testing.T) {
	t.Setenv("REBUNO_LOG_LEVEL", "debug")
	t.Setenv("REBUNO_LOG_FORMAT", "json")
	t.Setenv("REBUNO_OTEL_ENDPOINT", "otel:4317")
	t.Setenv("REBUNO_OTEL_SAMPLE_RATE", "0.25")
	t.Setenv("REBUNO_OTEL_INSECURE", "true")
	t.Setenv("REBUNO_DB_MAX_CONNS", "40")
	c := FromEnv()
	if c.LogLevel != "debug" || c.LogFormat != "json" {
		t.Fatalf("logging not read: %+v", c)
	}
	if c.OTELEndpoint != "otel:4317" || c.OTELSampleRate != 0.25 || !c.OTELInsecure {
		t.Fatalf("otel not read: %+v", c)
	}
	if c.DBMaxConns != 40 {
		t.Fatalf("DBMaxConns = %d, want 40", c.DBMaxConns)
	}
}

func TestValidateServerMode(t *testing.T) {
	c := Config{DevMode: false}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing db-url")
	}
	c = Config{DevMode: false, DBURL: "postgres://x"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing bearer token")
	}
	c = Config{DevMode: false, DBURL: "postgres://x", AgentBearerToken: "tok"}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDevModeNoRequirements(t *testing.T) {
	c := Config{DevMode: true}
	if err := c.Validate(); err != nil {
		t.Fatalf("dev mode should validate: %v", err)
	}
}

func TestDefaultDeadlineCheckInterval(t *testing.T) {
	c := Default()
	if c.DeadlineCheckInterval != 30*time.Second {
		t.Fatalf("DeadlineCheckInterval = %v, want 30s", c.DeadlineCheckInterval)
	}
}

func TestFromEnvReadsDeadlineCheckInterval(t *testing.T) {
	t.Setenv("REBUNO_DEADLINE_CHECK_INTERVAL", "5s")
	c := FromEnv()
	if c.DeadlineCheckInterval != 5*time.Second {
		t.Fatalf("DeadlineCheckInterval = %v, want 5s", c.DeadlineCheckInterval)
	}
}
