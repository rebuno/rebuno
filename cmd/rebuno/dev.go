package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rebuno/rebuno/internal/config"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store/memstore"
	"github.com/rebuno/rebuno/internal/stream"
)

func bindDevFlags(f *pflag.FlagSet, cfg *config.Config, configPath *string) {
	f.StringVar(&cfg.ListenAddr, "listen-addr", cfg.ListenAddr, "HTTP listen address")
	f.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	f.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (json, text)")
	f.StringVar(configPath, "config", *configPath, "Path to a provisioning manifest registering agents and policies")
}

func devCmd() *cobra.Command {
	cfg := config.FromEnv()
	var configPath string
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Start a development kernel (in-memory, no auth, no dependencies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.DevMode = true
			cfg.AgentBearerToken = "" // auth disabled in dev
			return runDev(cfg, configPath)
		},
	}
	bindDevFlags(cmd.Flags(), &cfg, &configPath)
	return cmd
}

func runDev(cfg config.Config, configPath string) error {
	logger := observe.NewLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	s := memstore.NewStore()
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
		RateLimiter: ratelimit.NewMemoryLimiter(),
		Logger:      logger,
	}

	replicaID := "dev-" + time.Now().Format("20060102-150405")
	k := kernel.New(kernel.Config{ReplicaID: replicaID}, deps)

	agentsDesc := "none (use --config <file> or the REPL to register agents)"
	if configPath != "" {
		agents, err := loadAgentConfig(configPath)
		if err != nil {
			return err
		}
		if err := registerAgents(ctx, k, agents); err != nil {
			return err
		}
		agentsDesc = fmt.Sprintf("%d provisioned from %s", len(agents), configPath)
	}

	fmt.Printf("\nrebuno dev — development mode\n\n")
	fmt.Printf("  kernel    http://%s\n", cfg.ListenAddr)
	fmt.Printf("  agents    %s\n", agentsDesc)
	fmt.Printf("  storage   in-memory (data lost on restart)\n\n")

	if isInteractive() {
		go runREPL(ctx, k, cancel)
	}

	hub := stream.NewHub(stream.NewMemoryBus())
	return serve(ctx, cfg, deps, logger, replicaID, nil, hub)
}
