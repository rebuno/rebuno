package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/config"
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/lifecycle"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/postgres"
	"github.com/rebuno/rebuno/internal/store"
	redisstore "github.com/rebuno/rebuno/internal/store/redis"
	"github.com/rebuno/rebuno/migrations"
)

func serverCmd() *cobra.Command {
	cfg := config.DefaultConfig()

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the production kernel",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.ApplyEnv()
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("config: %w", err)
			}
			return runServer(cfg)
		},
	}

	f := cmd.Flags()
	f.IntVar(&cfg.Port, "port", cfg.Port, "Server port")
	f.StringVar(&cfg.Bind, "bind", cfg.Bind, "Bind address")
	f.BoolVar(&cfg.Production, "production", cfg.Production, "Enable production mode")
	f.StringVar(&cfg.PolicyFile, "policy", "", "Policy configuration file path")
	f.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file path")
	f.StringVar(&cfg.TLSKey, "tls-key", "", "TLS key file path")
	f.StringVar(&cfg.DatabaseURL, "db-url", "", "PostgreSQL connection URL")
	f.IntVar(&cfg.DBMaxConns, "db-max-conns", 0, "Database connection pool max size")
	f.IntVar(&cfg.DBMinConns, "db-min-conns", 0, "Database connection pool min size")
	f.DurationVar(&cfg.ExecutionTimeout, "execution-timeout", cfg.ExecutionTimeout, "Execution timeout")
	f.DurationVar(&cfg.StepTimeout, "step-timeout", cfg.StepTimeout, "Step timeout")
	f.DurationVar(&cfg.AgentTimeout, "agent-timeout", cfg.AgentTimeout, "Agent timeout")
	f.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level")
	f.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (json, text)")
	f.StringVar(&cfg.OTELEndpoint, "otel-endpoint", "", "OTLP gRPC endpoint")
	f.Float64Var(&cfg.OTELSampleRate, "otel-sample-rate", cfg.OTELSampleRate, "OTEL sample rate")
	f.BoolVar(&cfg.OTELInsecure, "otel-insecure", cfg.OTELInsecure, "Insecure OTLP connection")
	f.DurationVar(&cfg.RetentionPeriod, "retention-period", cfg.RetentionPeriod, "Retention period for terminal executions")
	f.DurationVar(&cfg.CleanupInterval, "cleanup-interval", cfg.CleanupInterval, "Cleanup interval")
	f.DurationVar(&cfg.RetryBaseDelay, "retry-base-delay", cfg.RetryBaseDelay, "Base delay for retries")
	f.DurationVar(&cfg.RetryMaxDelay, "retry-max-delay", cfg.RetryMaxDelay, "Max delay for retries")
	f.StringVar(&cfg.BearerToken, "bearer-token", "", "Bearer token for API authentication")
	f.StringVar(&cfg.CORSOrigins, "cors-origins", "", "Comma-separated CORS origins")
	f.StringVar(&cfg.RedisURL, "redis-url", "", "Redis URL for persistent job queue (optional)")

	return cmd
}

func runServer(cfg *config.Config) error {
	logger := observe.NewLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	shutdownTracer, err := observe.InitTracer(ctx, cfg.OTELEndpoint, cfg.OTELSampleRate, cfg.OTELInsecure, logger)
	if err != nil {
		return fmt.Errorf("initializing tracer: %w", err)
	}
	defer shutdownTracer(context.Background())

	metrics := observe.NewMetrics()

	if cfg.DatabaseURL == "" {
		return fmt.Errorf("--db-url is required")
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, postgres.PoolConfig{
		MaxConns: int32(cfg.DBMaxConns),
		MinConns: int32(cfg.DBMinConns),
	})
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, migrations.FS, "."); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database connected and migrated")

	eventStore := postgres.NewEventStore(pool)
	checkpointStore := postgres.NewCheckpointStore(pool)
	signalStore := postgres.NewSignalStore(pool)
	sessionStore := postgres.NewSessionStore(pool)
	runnerStore := postgres.NewRunnerStore(pool)
	locker := postgres.NewLocker(pool)

	agentHub := hub.New(logger)
	defer agentHub.Close()
	runnerHub := hub.NewRunnerHub(logger)
	defer runnerHub.Close()

	var policyEngine policy.Engine
	if cfg.PolicyFile != "" {
		info, err := os.Stat(cfg.PolicyFile)
		if err != nil {
			return fmt.Errorf("reading policy path: %w", err)
		}
		if info.IsDir() {
			result, err := policy.LoadDir(cfg.PolicyFile)
			if err != nil {
				return fmt.Errorf("loading policy directory: %w", err)
			}
			agentEngine, err := policy.NewAgentEngine(result)
			if err != nil {
				return fmt.Errorf("creating agent engine: %w", err)
			}
			policyEngine = policy.NewSecureDefaultEngine(agentEngine)
			logger.Info("policy loaded from directory", "path", cfg.PolicyFile, "agents", agentEngine.Agents(), "has_global", result.Global != nil)
		} else {
			policyCfg, err := policy.Load(cfg.PolicyFile)
			if err != nil {
				return fmt.Errorf("loading policy: %w", err)
			}
			ruleEngine, err := policy.NewRuleEngine(*policyCfg)
			if err != nil {
				return fmt.Errorf("creating rule engine: %w", err)
			}
			policyEngine = policy.NewSecureDefaultEngine(ruleEngine)
			logger.Info("policy loaded", "path", cfg.PolicyFile, "rules", len(policyCfg.Rules))
		}
	} else {
		policyEngine = policy.NewSecureDefaultEngine(nil)
		if !cfg.Production {
			logger.Warn("no policy file configured, using secure defaults")
		}
	}

	var jobQueue store.JobQueue
	if cfg.RedisURL != "" {
		redisClient, err := redisstore.NewClient(ctx, cfg.RedisURL)
		if err != nil {
			return fmt.Errorf("connecting to redis: %w", err)
		}
		defer redisClient.Close()
		jobQueue = redisstore.NewJobQueue(redisClient)
		logger.Info("redis job queue enabled")
	}

	k := kernel.NewKernel(kernel.Deps{
		Events:      eventStore,
		Checkpoints: checkpointStore,
		AgentHub:    agentHub,
		RunnerHub:   runnerHub,
		Signals:     signalStore,
		Sessions:    sessionStore,
		Runners:     runnerStore,
		Locker:      locker,
		Policy:      policyEngine,
		Logger:      logger,
		Metrics:     metrics,
		JobQueue:    jobQueue,
		Config: kernel.KernelConfig{
			ExecutionTimeout: cfg.ExecutionTimeout,
			StepTimeout:      cfg.StepTimeout,
			AgentTimeout:     cfg.AgentTimeout,
			RetryBaseDelay:   cfg.RetryBaseDelay,
			RetryMaxDelay:    cfg.RetryMaxDelay,
		},
	})
	defer k.Shutdown()

	lm := lifecycle.NewManager(lifecycle.Deps{
		Events:           eventStore,
		Sessions:         sessionStore,
		Checkpoints:      checkpointStore,
		Signals:          signalStore,
		AgentHub:         agentHub,
		Locker:           locker,
		Projector:        k.Projector(),
		Emitter:          k,
		Logger:           logger,
		ExecutionTimeout: cfg.ExecutionTimeout,
	})
	lm.StartSessionReaper(ctx)
	lm.StartTimeoutWatcher(ctx)
	lm.StartCleanup(ctx, cfg.RetentionPeriod, cfg.CleanupInterval)
	lm.RecoverActiveExecutions(ctx)

	srv := api.NewServer(api.ServerDeps{
		Kernel:      k,
		Pool:        pool,
		Hub:         agentHub,
		RunnerHub:   runnerHub,
		Logger:      logger,
		BearerToken: cfg.BearerToken,
		CORSOrigins: cfg.CORSOrigins,
	})

	addr := cfg.ListenAddr()
	errCh := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			errCh <- srv.ListenAndServeTLS(addr, cfg.TLSCert, cfg.TLSKey)
		} else {
			errCh <- srv.ListenAndServe(addr)
		}
	}()

	logger.Info("rebuno kernel started", "addr", addr, "production", cfg.Production)

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	logger.Info("rebuno kernel stopped")
	return nil
}
