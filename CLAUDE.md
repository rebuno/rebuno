## What is Rebuno

Rebuno is a kernel-authoritative execution runtime for AI agents. It sits between agents and the tools they use, providing policy control, audit trails, and operational visibility. Agents propose intents (invoke_tool, wait, complete, fail); the kernel validates, enforces policy, and records every decision as an immutable event.

## Build & Development Commands

```bash
make build              # Build kernel binary to bin/rebuno
make dev                # Build + run in dev mode (in-memory store, permissive)
make test               # Unit tests: go test -race ./...
make test-integration   # Integration tests (requires Docker for testcontainers)
make lint               # golangci-lint run ./...
make fmt                # gofmt -s -w .
make tidy               # go mod tidy
```

Run a single test:
```bash
go test -run TestName ./internal/api/...
```

Integration tests use build tag `integration` and testcontainers for ephemeral PostgreSQL:
```bash
go test -tags integration -count=1 -v ./tests/integration/...
```

### Core execution flow

1. Agent connects via SSE, registers tools
2. Client creates an execution with input
3. Kernel dispatches execution to agent
4. Agent proposes intents (invoke_tool, wait, complete, fail)
5. Kernel evaluates policy rules, records events, transitions state
6. For remote tools: kernel dispatches to runners via job queue

### Execution state machine

`pending` → `running` ↔ `blocked` → `completed` | `failed` | `cancelled`

### Key packages (`internal/`)

- **domain/** — Core types: Execution, Intent, Step, Event, Policy, Session, Signal, Runner
- **api/** — HTTP handlers (chi router), SSE streams, middleware (auth, tracing)
- **kernel/** — Core execution engine and state transition logic
- **hub/** — Agent and runner session management (agenthub, runnerhub)
- **store/** — Persistence interfaces (event store, checkpoint store, job queue)
- **memstore/** — In-memory store implementation (dev mode)
- **postgres/** — PostgreSQL driver, connection pool, migrations
- **store/redis/** — Redis-backed job queue (optional, production)
- **policy/** — YAML policy parsing and evaluation engine
- **lifecycle/** — Execution state machine transitions
- **observe/** — OpenTelemetry tracing, Prometheus metrics
- **config/** — Configuration management

### Entry point

`cmd/rebuno/main.go` — Cobra CLI with commands: `dev`, `server`, `version`, plus inspection commands (events, executions)
