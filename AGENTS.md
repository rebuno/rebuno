# Working on Rebuno

Rebuno is a Go execution runtime for durable agents. The kernel owns execution
state and records effects as steps; external agents run over signed HTTP webhooks.
Read [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution workflow and
[docs/architecture.md](docs/architecture.md) before changing runtime behavior.

## Repository map

| Path | Responsibility |
| --- | --- |
| `cmd/rebuno/` | Cobra CLI, dev/server startup, dependency wiring, agent provisioning. |
| `internal/kernel/` | Execution and step transitions, replay, approvals, dispatch coordination. |
| `internal/domain/`, `internal/identity/`, `internal/payload/` | Shared types and errors, deterministic step IDs, event payloads. |
| `internal/store/` | Storage interfaces, `postgres/` production backend, `memstore/` dev/test backend. |
| `internal/api/` | HTTP routes, authentication, request/response handling, SSE endpoints. |
| `internal/policy/`, `internal/ratelimit/`, `internal/usage/` | Policy bundles, rate limits, token usage accounting. |
| `internal/dispatcher/`, `internal/lifecycle/` | Webhook delivery and background workers. |
| `internal/stream/`, `internal/observe/`, `internal/config/` | Live deltas, metrics/tracing/logging, configuration. |
| `migrations/`, `deploy/` | Embedded Goose SQL migrations and deployment configuration. |
| `docs/`, `examples/`, `skills/rebuno/` | User documentation, runnable examples, guidance for building agents with Rebuno. |

The Python and TypeScript SDK implementations and dashboard are outside this
repository. `docs/sdk/` contains SDK documentation; `examples/` uses published
SDK packages. Do not treat installed dependencies or local caches as source.

## Development and validation

Use Go 1.26+ as specified in `go.mod`. Run commands from the repository root:

| Command | Purpose |
| --- | --- |
| `make build` | Build `bin/rebuno`. |
| `make dev` | Build and start the in-memory kernel without seeded agents. |
| `go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml` | Start the dev kernel with the example agents and policies. |
| `go test -race ./internal/kernel/...` | Example of focused package tests; adjust to the affected packages. |
| `make test` | Run `go test -race ./...`. |
| `make lint` | Run the pinned golangci-lint version from the Makefile. |
| `make fmt` | Run `gofmt -s -w .`; format only changed Go files when unrelated edits are present. |
| `make tidy` | Run `go mod tidy` when dependencies change. |

For Go changes, run focused tests while iterating, then `make test` and
`make lint` before handing off. CI runs the race-enabled suite with Postgres 17
and the pinned linter; see [.github/workflows/ci.yml](.github/workflows/ci.yml).
For documentation-only changes, check paths, commands, and consistency with the
implementation; Go tests are unnecessary.

Postgres store and stream integration tests require `DATABASE_URL` and skip their
database coverage without it or with `-short`. Use a dedicated test database:
store tests apply migrations and write data. Report when database coverage was
skipped, or when any check could not run, along with the reason.

## Runtime invariants

- Keep execution and step state changes in the kernel. HTTP handlers should
  validate and translate requests, then call kernel interfaces.
- Preserve atomic writes: events, projections, and any resulting dispatch must
  commit together through `store.UnitOfWork.RunInTx`. Use the provided
  `store.TxStore` inside the callback. Follow the existing execution-lock pattern
  when mutating an existing execution; a process-local mutex cannot serialize
  writes across replicas.
- Fence agent writes with the dispatch ID and attempt in the same transaction
  as the mutation. Reclaimed or superseded attempts must not modify state.
- Preserve step identity and replay semantics. IDs depend on execution, kind,
  target, canonical argument hash, and occurrence. Occurrence counts reset on
  each delivery attempt. Replaying a recorded outcome must not rerun the effect
  or reevaluate policy, and terminal outcomes must not be overwritten.
- Preserve the distinction between `safe_to_retry` and `at_most_once` recovery.
  An orphaned `at_most_once` effect has an indeterminate outcome; it must not be
  silently retried. The runtime does not promise exactly-once external effects.
- Policy gates calls without rewriting their arguments. Keep allow, deny,
  approval, rate-limit, and budget behavior consistent with the documented
  state machines. Approval blocking and rate-limit waiting have different
  execution-state semantics.
- Keep live token deltas separate from durable events and step results. Follow
  [docs/streaming.md](docs/streaming.md) when changing streaming behavior.

## Implementation and tests

- Follow the surrounding Go code: small interfaces, explicit dependencies,
  `context.Context` propagation, domain errors, and structured `slog` logging.
  Reuse event payload helpers and HTTP response/error helpers.
- When changing storage contracts, update both Postgres and the in-memory
  implementation. **The in-memory `RunInTx` does not roll back on error.** Use
  Postgres integration tests to validate rollback, SQL, and database locking.
- Add regression tests for behavior changes. For runtime changes, cover the
  relevant replay, duplicate delivery, stale lease, cancellation, and failure
  paths, not just a successful first execution. Reuse nearby test helpers.
- Keep schema changes in embedded Goose migrations under `migrations/` and
  account for upgrading an existing database as well as creating a fresh one.
- Treat `/v0` JSON fields, error codes, event payloads, webhook signatures, and
  policy YAML as contracts consumed by SDKs and the dashboard. Call out changes
  that require coordinated updates in those projects.

## Comments, tests, and documentation style

Write repository content for someone reading the finished system with no access
to the task discussion. Changes should read as a natural part of the codebase.

- Keep comments and docstrings concise. Explain non-obvious intent, invariants,
  or constraints when the code cannot express them clearly. Omit comments that
  restate the code or announce an edit.
- Keep conversation references, review replies, task instructions, and abandoned
  approaches out of code, tests, and documentation. Put implementation history
  and change rationale in PR descriptions or commit messages.
- Describe behavior directly in the present tense. Avoid change-relative wording
  such as "now", "new", "previously", "we changed", or "X instead of Y" when it
  only makes sense in the context of the change. Explain a comparison only when
  it helps the reader understand a lasting distinction or compatibility rule.
- Update existing documentation and examples in place. Integrate the final
  behavior into the relevant section; avoid appended fix notes, repeated caveats,
  and explanations of superseded designs. Release notes and migration guides
  can describe changes over time when that is their purpose.
- Name tests for the behavior or invariant they verify. Keep assertions focused
  on meaningful outcomes and failure modes. Preserve useful regression coverage;
  avoid redundant tests, assertions that merely mirror implementation details,
  and test commentary that recounts the debugging session.
- Review the diff for wording that depends on knowing the conversation or the
  previous patch. Remove it or rewrite it as a standalone explanation of the
  current system.

## Documentation that changes with the code

Update the relevant documentation in the same change:

- Runtime or replay behavior: `docs/architecture.md`, `docs/agents.md`,
  `docs/tools.md`, and `docs/events.md` as applicable.
- HTTP, policy, or streaming behavior: `docs/api.md`, `docs/policy.md`, or
  `docs/streaming.md`.
- CLI, configuration, or deployment: `docs/cli.md`, `docs/deployment.md`, and
  affected files in `deploy/` or `examples/`.
- Agent-facing workflows: applicable `docs/sdk/` pages, examples, and
  `skills/rebuno/references/`.

Keep this file focused on lasting repository guidance. Put detailed explanations
in the existing docs and link to them here.
