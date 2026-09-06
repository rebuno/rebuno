# Working on Rebuno

Rebuno's Go kernel owns durable execution state and policy enforcement. Agents
run as external services over signed webhooks. The SDK implementations and
dashboard are separate repositories; their user documentation lives here.

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and commands, and
[docs/architecture.md](docs/architecture.md) before changing runtime behavior.

## Development and validation

Use the Go version in `go.mod` and the tools in the [Makefile](Makefile).
For Go changes, run focused tests while iterating, then `make test` and
`make lint`. Format changed Go files. For documentation-only changes, check
paths, commands, and implementation consistency; Go tests are unnecessary.
Report checks that could not run and why.

Postgres integration coverage requires `DATABASE_URL` and is skipped without it
or with `-short`. Use a dedicated test database: tests apply migrations and write
data. Report skipped database coverage. **The in-memory store does not roll back
failed transactions**; use Postgres tests for rollback, SQL, and database locking.

## Runtime constraints

- Keep execution and step transitions in the kernel. Commit events, projections,
  and resulting dispatches together through the transaction's store. Preserve
  execution locking across replicas and validate dispatch leases atomically
  with agent mutations.
- Preserve deterministic step identity and replay. Recorded outcomes must not
  rerun effects or reevaluate policy; terminal outcomes must not be overwritten.
  Keep `safe_to_retry` and `at_most_once` recovery distinct. An orphaned
  `at_most_once` effect must not be silently retried.
- Keep policy decisions and state transitions consistent with the documented
  state machines. Live stream deltas are separate from durable step results.
- Update both storage backends when storage contracts change. Schema changes
  must account for existing databases as well as fresh installations.
- Treat API fields, error codes, event payloads, signatures, and policy YAML as
  contracts shared with the SDKs and dashboard. Identify coordinated changes
  those projects need. Test affected replay, stale-lease, and failure paths.

Update relevant docs, examples, and agent-building guidance with behavior changes.
Use the [README documentation index](README.md#documentation) to find the relevant
reference; keep detailed architecture and protocol explanations there.

## Comments, tests, and documentation style

Write for someone reading the finished system with no access to the task
conversation. Changes should read as a natural part of the codebase.

- Keep comments and docstrings sparse and concise. Explain non-obvious intent,
  invariants, or constraints; omit restatements of code and announcements of edits.
- Keep conversation references, review replies, and abandoned approaches out of
  source, tests, and docs. Put change history in PRs, commits, release notes, or
  migration guides when relevant.
- Describe current behavior directly in the present tense. Avoid change-relative
  wording such as "now", "previously", or "X instead of Y" unless it explains a
  lasting distinction or compatibility rule.
- Update existing documentation and examples in place. Avoid appended fix notes
  and repeated caveats. Review additions for wording that depends on the task
  discussion or previous patch.
- Add focused regression tests for meaningful behavior and failure modes. Name
  tests for the behavior they verify; avoid redundant coverage, assertions that
  mirror implementation details, and commentary about the debugging session.
