package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr             string
	AgentBearerToken       string
	DevMode                bool
	DBURL                  string
	DispatchMaxAttempts    int
	DispatchBaseDelay      time.Duration
	DispatchMaxDelay       time.Duration
	DispatchTimeout        time.Duration
	DispatchConcurrency    int
	DispatchLeaseTimeout   time.Duration
	DefaultApprovalTimeout time.Duration
	DeadlineTimeout        time.Duration
	CleanupInterval        time.Duration
	Retention              time.Duration
	LeaderLockKey          string
	LogLevel               string
	LogFormat              string
	OTELEndpoint           string
	OTELSampleRate         float64
	OTELInsecure           bool
	DBMaxConns             int
	DBMinConns             int
}

func Default() Config {
	return Config{
		ListenAddr:             ":8080",
		DispatchMaxAttempts:    5,
		DispatchBaseDelay:      1 * time.Second,
		DispatchMaxDelay:       30 * time.Second,
		DispatchTimeout:        30 * time.Second,
		DispatchConcurrency:    8,
		DefaultApprovalTimeout: 15 * time.Minute,
		CleanupInterval:        10 * time.Minute,
		Retention:              24 * time.Hour,
		LeaderLockKey:          "rebuno_scheduler_leader",
		LogLevel:               "info",
		LogFormat:              "text",
		OTELSampleRate:         1.0,
	}
}

func FromEnv() Config {
	cfg := Default()
	if v := os.Getenv("REBUNO_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("REBUNO_BEARER_TOKEN"); v != "" {
		cfg.AgentBearerToken = v
	}
	if v := os.Getenv("REBUNO_DB_URL"); v != "" {
		cfg.DBURL = v
	}
	if v := os.Getenv("REBUNO_DEV"); v == "true" || v == "1" {
		cfg.DevMode = true
	}
	if v := os.Getenv("REBUNO_DISPATCH_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.DispatchMaxAttempts = n
		}
	}
	if v := os.Getenv("REBUNO_DISPATCH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DispatchTimeout = d
		}
	}
	if v := os.Getenv("REBUNO_DISPATCH_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DispatchConcurrency = n
		}
	}
	if v := os.Getenv("REBUNO_DISPATCH_LEASE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DispatchLeaseTimeout = d
		}
	}
	if v := os.Getenv("REBUNO_DEADLINE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DeadlineTimeout = d
		}
	}
	if v := os.Getenv("REBUNO_APPROVAL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DefaultApprovalTimeout = d
		}
	}
	if v := os.Getenv("REBUNO_CLEANUP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.CleanupInterval = d
		}
	}
	if v := os.Getenv("REBUNO_RETENTION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Retention = d
		}
	}
	if v := os.Getenv("REBUNO_LEADER_LOCK_KEY"); v != "" {
		cfg.LeaderLockKey = v
	}
	if v := os.Getenv("REBUNO_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("REBUNO_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := os.Getenv("REBUNO_OTEL_ENDPOINT"); v != "" {
		cfg.OTELEndpoint = v
	}
	if v := os.Getenv("REBUNO_OTEL_SAMPLE_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.OTELSampleRate = f
		}
	}
	if v := os.Getenv("REBUNO_OTEL_INSECURE"); v == "true" || v == "1" {
		cfg.OTELInsecure = true
	}
	if v := os.Getenv("REBUNO_DB_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.DBMaxConns = n
		}
	}
	if v := os.Getenv("REBUNO_DB_MIN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.DBMinConns = n
		}
	}
	return cfg
}

func (c Config) Validate() error {
	if c.DevMode {
		return nil
	}
	if c.DBURL == "" {
		return fmt.Errorf("REBUNO_DB_URL (--db-url) required in server mode")
	}
	if c.AgentBearerToken == "" {
		return fmt.Errorf("REBUNO_BEARER_TOKEN (--bearer-token) required in server mode")
	}
	return nil
}
