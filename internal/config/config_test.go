package config

import (
	"testing"
	"time"
)

func TestFromEnvOverridesDefaults(t *testing.T) {
	t.Setenv("REBUNO_LOG_LEVEL", "debug")
	t.Setenv("REBUNO_LOG_FORMAT", "json")
	t.Setenv("REBUNO_OTEL_ENDPOINT", "otel:4317")
	t.Setenv("REBUNO_OTEL_SAMPLE_RATE", "0.25")
	t.Setenv("REBUNO_OTEL_INSECURE", "true")
	t.Setenv("REBUNO_DB_MAX_CONNS", "40")
	t.Setenv("REBUNO_DEADLINE_CHECK_INTERVAL", "5s")

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
	if c.DeadlineCheckInterval != 5*time.Second {
		t.Fatalf("DeadlineCheckInterval = %v, want 5s", c.DeadlineCheckInterval)
	}
}

func TestValidateServerMode(t *testing.T) {
	c := Config{}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing db-url")
	}
	c = Config{DBURL: "postgres://x"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing bearer token")
	}
	c = Config{DBURL: "postgres://x", AgentBearerToken: "tok"}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
