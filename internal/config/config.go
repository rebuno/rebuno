package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type Config struct {
	Port int
	Bind string

	Production bool
	PolicyFile string
	TLSCert    string
	TLSKey     string

	DatabaseURL string
	DBMaxConns  int
	DBMinConns  int

	ExecutionTimeout time.Duration
	StepTimeout      time.Duration
	AgentTimeout     time.Duration

	LogLevel       string
	LogFormat      string
	OTELEndpoint   string
	OTELSampleRate float64
	OTELInsecure   bool

	RetentionPeriod time.Duration
	CleanupInterval time.Duration

	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	BearerToken string
	CORSOrigins string

	RedisURL string
}

func DefaultConfig() *Config {
	return &Config{
		Port:             8080,
		Bind:             "0.0.0.0",
		ExecutionTimeout: time.Hour,
		StepTimeout:      5 * time.Minute,
		AgentTimeout:     30 * time.Second,
		LogLevel:         "info",
		LogFormat:        "json",
		OTELSampleRate:   0.1,
		OTELInsecure:     true,
		RetentionPeriod:  168 * time.Hour,
		CleanupInterval:  time.Hour,
		RetryBaseDelay:   time.Second,
		RetryMaxDelay:    30 * time.Second,
	}
}

func (c *Config) ApplyEnv() {
	if v := os.Getenv("REBUNO_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err != nil {
			log.Printf("WARNING: invalid REBUNO_PORT value %q, using default %d", v, c.Port)
		} else {
			c.Port = port
		}
	}
	if v := os.Getenv("REBUNO_BIND"); v != "" {
		c.Bind = v
	}
	if v := os.Getenv("REBUNO_PRODUCTION"); v != "" {
		c.Production = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("REBUNO_POLICY"); v != "" {
		c.PolicyFile = v
	}
	if v := os.Getenv("REBUNO_TLS_CERT"); v != "" {
		c.TLSCert = v
	}
	if v := os.Getenv("REBUNO_TLS_KEY"); v != "" {
		c.TLSKey = v
	}
	if v := os.Getenv("REBUNO_DB_URL"); v != "" {
		c.DatabaseURL = v
	}
	if v := os.Getenv("REBUNO_DB_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.DBMaxConns = n
		} else {
			log.Printf("WARNING: invalid REBUNO_DB_MAX_CONNS value %q, using default %d", v, c.DBMaxConns)
		}
	}
	if v := os.Getenv("REBUNO_DB_MIN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.DBMinConns = n
		} else {
			log.Printf("WARNING: invalid REBUNO_DB_MIN_CONNS value %q, using default %d", v, c.DBMinConns)
		}
	}
	if v := os.Getenv("REBUNO_EXECUTION_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.ExecutionTimeout = d
		} else {
			log.Printf("WARNING: invalid REBUNO_EXECUTION_TIMEOUT value %q, using default %s", v, c.ExecutionTimeout)
		}
	}
	if v := os.Getenv("REBUNO_STEP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.StepTimeout = d
		} else {
			log.Printf("WARNING: invalid REBUNO_STEP_TIMEOUT value %q, using default %s", v, c.StepTimeout)
		}
	}
	if v := os.Getenv("REBUNO_AGENT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.AgentTimeout = d
		} else {
			log.Printf("WARNING: invalid REBUNO_AGENT_TIMEOUT value %q, using default %s", v, c.AgentTimeout)
		}
	}
	if v := os.Getenv("REBUNO_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("REBUNO_LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
	if v := os.Getenv("REBUNO_OTEL_ENDPOINT"); v != "" {
		c.OTELEndpoint = v
	}
	if v := os.Getenv("REBUNO_OTEL_INSECURE"); v != "" {
		c.OTELInsecure = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("REBUNO_OTEL_SAMPLE_RATE"); v != "" {
		if rate, err := strconv.ParseFloat(v, 64); err != nil {
			log.Printf("WARNING: invalid REBUNO_OTEL_SAMPLE_RATE value %q, using default %f", v, c.OTELSampleRate)
		} else {
			c.OTELSampleRate = rate
		}
	}
	if v := os.Getenv("REBUNO_RETENTION_PERIOD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.RetentionPeriod = d
		} else {
			log.Printf("WARNING: invalid REBUNO_RETENTION_PERIOD value %q, using default %s", v, c.RetentionPeriod)
		}
	}
	if v := os.Getenv("REBUNO_CLEANUP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.CleanupInterval = d
		} else {
			log.Printf("WARNING: invalid REBUNO_CLEANUP_INTERVAL value %q, using default %s", v, c.CleanupInterval)
		}
	}
	if v := os.Getenv("REBUNO_RETRY_BASE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.RetryBaseDelay = d
		} else {
			log.Printf("WARNING: invalid REBUNO_RETRY_BASE_DELAY value %q, using default %s", v, c.RetryBaseDelay)
		}
	}
	if v := os.Getenv("REBUNO_RETRY_MAX_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.RetryMaxDelay = d
		} else {
			log.Printf("WARNING: invalid REBUNO_RETRY_MAX_DELAY value %q, using default %s", v, c.RetryMaxDelay)
		}
	}
	if v := os.Getenv("REBUNO_BEARER_TOKEN"); v != "" {
		c.BearerToken = v
	}
	if v := os.Getenv("REBUNO_CORS_ORIGINS"); v != "" {
		c.CORSOrigins = v
	}
	if v := os.Getenv("REBUNO_REDIS_URL"); v != "" {
		c.RedisURL = v
	}
}

func (c *Config) Validate() error {
	if c.Production {
		if c.PolicyFile == "" {
			return fmt.Errorf("%w: --policy required in production mode", domain.ErrInvalidConfiguration)
		}
		if c.DatabaseURL == "" {
			return fmt.Errorf("%w: --db-url required in production mode", domain.ErrInvalidConfiguration)
		}
		if c.BearerToken == "" {
			return fmt.Errorf("%w: --bearer-token required in production mode", domain.ErrInvalidConfiguration)
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%w: invalid log level %q", domain.ErrInvalidConfiguration, c.LogLevel)
	}

	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("%w: invalid log format %q", domain.ErrInvalidConfiguration, c.LogFormat)
	}

	return nil
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}
