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
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/lifecycle"
	"github.com/rebuno/rebuno/internal/memstore"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/policy"
)

func devCmd() *cobra.Command {
	var (
		port        int
		bind        string
		policyFile  string
		corsOrigins string
		logLevel    string
		logFormat   string
	)

	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Start a development kernel (in-memory, no dependencies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(port, bind, policyFile, corsOrigins, logLevel, logFormat)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "Kernel listen port")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "Bind address")
	cmd.Flags().StringVar(&policyFile, "policy", "", "Policy file or directory path")
	cmd.Flags().StringVar(&corsOrigins, "cors-origins", "*", "CORS origins")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "Log level")
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "Log format (json, text)")

	return cmd
}

func runDev(port int, bind, policyFile, corsOrigins, logLevel, logFormat string) error {
	logger := observe.NewLogger(logLevel, logFormat)
	slog.SetDefault(logger)

	addr := fmt.Sprintf("%s:%d", bind, port)
	policyStatus := "permissive (all tools allowed)"

	eventStore := memstore.NewEventStore()
	checkpointStore := memstore.NewCheckpointStore()
	signalStore := memstore.NewSignalStore()
	sessionStore := memstore.NewSessionStore()
	runnerStore := memstore.NewRunnerStore()
	locker := memstore.NewLocker()

	agentHub := hub.New(logger)
	defer agentHub.Close()
	runnerHub := hub.NewRunnerHub(logger)
	defer runnerHub.Close()

	var policyEngine policy.Engine
	if policyFile != "" {
		info, err := os.Stat(policyFile)
		if err != nil {
			return fmt.Errorf("reading policy path: %w", err)
		}
		if info.IsDir() {
			result, err := policy.LoadDir(policyFile)
			if err != nil {
				return fmt.Errorf("loading policy directory: %w", err)
			}
			agentEngine, err := policy.NewAgentEngine(result)
			if err != nil {
				return fmt.Errorf("creating agent engine: %w", err)
			}
			policyEngine = policy.NewSecureDefaultEngine(agentEngine)
			policyStatus = fmt.Sprintf("%s (%d agents loaded)", policyFile, len(agentEngine.Agents()))
		} else {
			policyCfg, err := policy.Load(policyFile)
			if err != nil {
				return fmt.Errorf("loading policy: %w", err)
			}
			ruleEngine, err := policy.NewRuleEngine(*policyCfg)
			if err != nil {
				return fmt.Errorf("creating rule engine: %w", err)
			}
			policyEngine = policy.NewSecureDefaultEngine(ruleEngine)
			policyStatus = fmt.Sprintf("%s (%d rules loaded)", policyFile, len(policyCfg.Rules))
		}
	} else {
		policyEngine = policy.NewPermissiveEngine(logger)
	}

	fmt.Printf("\nrebuno dev — development mode\n\n")
	fmt.Printf("  kernel    http://%s\n", addr)
	fmt.Printf("  policy    %s\n", policyStatus)
	fmt.Printf("  storage   in-memory (data lost on restart)\n\n")
	fmt.Printf("  Waiting for agents...\n\n")

	metrics := observe.NewMetrics()
	kcfg := kernel.DefaultKernelConfig()

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
		Config:      kcfg,
	})
	defer k.Shutdown()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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
		ExecutionTimeout: kcfg.ExecutionTimeout,
	})
	lm.StartSessionReaper(ctx)
	lm.StartTimeoutWatcher(ctx)
	lm.RecoverActiveExecutions(ctx)

	srv := api.NewServer(api.ServerDeps{
		Kernel:      k,
		Pool:        nil, // no database in dev mode
		Hub:         agentHub,
		RunnerHub:   runnerHub,
		Logger:      logger,
		BearerToken: "",
		CORSOrigins: corsOrigins,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(addr)
	}()

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

	return nil
}
