//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/postgres"
	"github.com/rebuno/rebuno/migrations"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var dsn string

	// Allow overriding with an external database (e.g. CI service containers)
	if envDSN := os.Getenv("REBUNO_TEST_DATABASE_URL"); envDSN != "" {
		dsn = envDSN
	} else {
		// Spin up a postgres container via testcontainers
		pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("rebuno_test"),
			tcpostgres.WithUsername("rebuno"),
			tcpostgres.WithPassword("rebuno"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
			os.Exit(1)
		}
		defer pg.Terminate(context.Background())

		dsn, err = pg.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintf(os.Stderr, "get connection string: %v\n", err)
			os.Exit(1)
		}
	}

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to test database: %v\n", err)
		os.Exit(1)
	}

	if err := postgres.Migrate(ctx, pool, migrations.FS, "."); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}

	if err := truncateAll(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "truncate failed: %v\n", err)
		os.Exit(1)
	}

	testPool = pool
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

type testServer struct {
	URL       string
	Shutdown  func()
	RunnerHub *hub.RunnerHub
}

func startServer(t *testing.T, pool *pgxpool.Pool) testServer {
	t.Helper()
	return startServerWithPolicy(t, pool, allowAllPolicy())
}

func startServerWithPolicy(t *testing.T, pool *pgxpool.Pool, policyEngine policy.Engine) testServer {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	eventStore := postgres.NewEventStore(pool)
	checkpointStore := postgres.NewCheckpointStore(pool)
	signalStore := postgres.NewSignalStore(pool)
	sessionStore := postgres.NewSessionStore(pool)
	runnerStore := postgres.NewRunnerStore(pool)
	locker := postgres.NewLocker(pool)

	agentHub := hub.New(logger)
	runnerHub := hub.NewRunnerHub(logger)

	metrics := observe.NewMetrics()

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
		Config: kernel.KernelConfig{
			ExecutionTimeout: 5 * time.Minute,
			StepTimeout:      30 * time.Second,
			AgentTimeout:     10 * time.Second,
		},
		Logger:  logger,
		Metrics: metrics,
	})

	srv := api.NewServer(api.ServerDeps{
		Kernel:    k,
		Pool:      pool,
		Hub:       agentHub,
		RunnerHub: runnerHub,
		Logger:    logger,
	})

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.ReadTimeout = 60 * time.Second
	ts.Config.WriteTimeout = 0
	ts.Listener, _ = net.Listen("tcp", "127.0.0.1:0")
	ts.Start()

	shutdown := func() {
		ts.Close()
		agentHub.Close()
		runnerHub.Close()
		k.Close()
	}
	t.Cleanup(shutdown)

	return testServer{
		URL:       ts.URL,
		Shutdown:  shutdown,
		RunnerHub: runnerHub,
	}
}

func allowAllPolicy() policy.Engine {
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-all",
				Priority: 0,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "integration test: allow all",
				},
			},
		},
		Default: domain.PolicyAction{
			Decision: domain.PolicyAllow,
			Reason:   "integration test: default allow",
		},
	}
	engine, _ := policy.NewRuleEngine(cfg)
	return engine
}

func waitForServer(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/v0/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %v", url, timeout)
}
