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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rebuno/kernel/internal/api"
	"github.com/rebuno/kernel/internal/config"
	"github.com/rebuno/kernel/internal/kernel"
	"github.com/rebuno/kernel/internal/lifecycle"
	"github.com/rebuno/kernel/internal/observe"
	"github.com/rebuno/kernel/internal/policy"
	"github.com/rebuno/kernel/internal/store/postgres"
)

// bindServerFlags binds server flags onto cfg. cfg should already be seeded
// from config.FromEnv() so that env values become the flag defaults and an
// explicitly-set flag overrides env (precedence: flag > env > default).
func bindServerFlags(f *pflag.FlagSet, cfg *config.Config) {
	f.StringVar(&cfg.ListenAddr, "listen-addr", cfg.ListenAddr, "HTTP listen address")
	f.StringVar(&cfg.DBURL, "db-url", cfg.DBURL, "PostgreSQL connection URL (required)")
	f.StringVar(&cfg.AgentBearerToken, "bearer-token", cfg.AgentBearerToken, "Bearer token for client/admin API (required)")
	f.IntVar(&cfg.DBMaxConns, "db-max-conns", cfg.DBMaxConns, "Max DB pool connections (0 = auto)")
	f.IntVar(&cfg.DBMinConns, "db-min-conns", cfg.DBMinConns, "Min DB pool connections")
	f.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	f.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (json, text)")
	f.StringVar(&cfg.OTELEndpoint, "otel-endpoint", cfg.OTELEndpoint, "OTLP gRPC endpoint (empty = tracing off)")
	f.Float64Var(&cfg.OTELSampleRate, "otel-sample-rate", cfg.OTELSampleRate, "Trace sample rate 0..1")
	f.BoolVar(&cfg.OTELInsecure, "otel-insecure", cfg.OTELInsecure, "Use insecure (plaintext) OTLP connection")
}

func serverCmd() *cobra.Command {
	cfg := config.FromEnv()
	var configPath string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the production kernel (Postgres-backed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.DevMode = false
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("config: %w", err)
			}
			return runServer(cfg, configPath)
		},
	}
	bindServerFlags(cmd.Flags(), &cfg)
	cmd.Flags().StringVar(&configPath, "config", configPath, "Path to a provisioning manifest registering agents and policies")
	return cmd
}

func runServer(cfg config.Config, configPath string) error {
	logger := observe.NewLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	shutdownTracer, err := observe.InitTracer(ctx, cfg.OTELEndpoint, cfg.OTELSampleRate, cfg.OTELInsecure, logger)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer func() { _ = shutdownTracer(context.Background()) }()

	pool, err := buildPool(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("database connected and migrated")

	s := postgres.NewStore(pool)

	// Provision agents and their policies from the manifest, if one was given.
	// RegisterAgent upserts, so this is idempotent across restarts and additive:
	// agents registered at runtime via the admin API are left untouched.
	if configPath != "" {
		agents, err := loadAgentConfig(configPath)
		if err != nil {
			return err
		}
		if err := registerAgents(ctx, s, agents); err != nil {
			return err
		}
		logger.Info("agents provisioned from config", "count", len(agents), "path", configPath)
	}

	deps := kernel.Deps{
		Events:      s,
		Steps:       s,
		Executions:  s,
		Agents:      s,
		Approvals:   s,
		Queue:       s,
		Locker:      s,
		UnitOfWork:  s,
		Policy:      policy.NewBundleResolver(s, policy.PermissiveEngine{}),
		RateLimiter: s,
		Logger:      logger,
	}

	replicaID, _ := os.Hostname()
	if replicaID == "" {
		replicaID = "rebuno"
	}

	return serve(ctx, cfg, deps, logger, replicaID, pool.Ping)
}

// buildPool parses the DB URL, raises MaxConns to a safe floor so per-execution
// advisory-lock holders cannot starve transactions of connections, applies any
// explicit pool overrides, and opens the pool.
func buildPool(ctx context.Context, cfg config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	if floor := int32(cfg.DispatchConcurrency*2 + 10); poolCfg.MaxConns < floor {
		poolCfg.MaxConns = floor
	}
	if cfg.DBMaxConns > 0 {
		poolCfg.MaxConns = int32(cfg.DBMaxConns)
	}
	if cfg.DBMinConns > 0 {
		poolCfg.MinConns = int32(cfg.DBMinConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open db pool: %w", err)
	}
	logger.Info("postgres pool configured", "max_conns", poolCfg.MaxConns)
	return pool, nil
}

// serve builds the kernel, starts the lifecycle manager and HTTP server, and
// blocks until a shutdown signal, then drains gracefully.
func serve(ctx context.Context, cfg config.Config, deps kernel.Deps, logger *slog.Logger, replicaID string, ready func(context.Context) error) error {
	k := kernel.New(kernel.Config{
		ReplicaID:                replicaID,
		DispatchMaxAttempts:      cfg.DispatchMaxAttempts,
		DispatchBaseDelay:        cfg.DispatchBaseDelay,
		DispatchMaxDelay:         cfg.DispatchMaxDelay,
		DispatchTimeout:          cfg.DispatchTimeout,
		DispatchConcurrency:      cfg.DispatchConcurrency,
		DefaultApprovalTimeout:   cfg.DefaultApprovalTimeout,
		ExecutionDeadlineTimeout: cfg.DeadlineTimeout,
		ExecutionCleanupInterval: cfg.CleanupInterval,
		ExecutionRetention:       cfg.Retention,
		DispatchLeaseTimeout:     cfg.DispatchLeaseTimeout,
		LeaderLockKey:            cfg.LeaderLockKey,
	}, deps)

	observer := observe.Default()
	adapt := &api.KernelAPI{Inner: k}
	handler := api.NewRouter(adapt, adapt, adapt, cfg.AgentBearerToken, ready, observer)
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler}

	mgr := lifecycle.NewManagerWithLocker(k, logger, cfg.CleanupInterval, deps.Locker, lifecycle.WithObserver(observer))
	mgr.LeaderLockKey = cfg.LeaderLockKey
	mgr.Retention = cfg.Retention
	mgr.Start(ctx)
	defer mgr.Stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("rebuno kernel listening", "addr", cfg.ListenAddr, "replica", replicaID)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}
	logger.Info("rebuno kernel stopped")
	return nil
}
