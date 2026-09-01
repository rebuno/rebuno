# CLI

```bash
rebuno dev
rebuno server --db-url DB_URL --bearer-token TOKEN
rebuno version
```

Replace `DB_URL` with a Postgres connection string and `TOKEN` with the bearer
token clients and admins present.

Both `dev` and `server` take `--config <config.yaml>` to register agents and
policies at boot, plus `--listen-addr`, `--log-level`, and `--log-format`. See
[Deployment](deployment.md) for the rest of the server's flags.

Build it with `make build`, which writes `bin/rebuno`, or run from source with
`go run ./cmd/rebuno …`.

## Reaching a kernel

| Variable | Default | Description |
|----------|---------|-------------|
| `REBUNO_URL` | `http://localhost:8080` | Kernel base URL. Override per command with `--url`. |
| `REBUNO_API_KEY` | none | Bearer token. Required against a kernel started with `rebuno server`. |

```bash
export REBUNO_URL=https://rebuno.internal
export REBUNO_API_KEY=…
rebuno exec ls
```

## Agents

| Command | Description |
|---------|-------------|
| `rebuno agent ls` | List registered agents. |
| `rebuno agent get <id>` | Show an agent and its policy bundle. |
| `rebuno agent add <config.yaml>` | Register one or more agents from a provisioning manifest. |
| `rebuno agent rm <id>` | Delete an agent. |

## Executions

| Command | Description |
|---------|-------------|
| `rebuno exec ls` | List executions, newest first. |
| `rebuno exec create <agent> [json]` | Start an execution (input defaults to `{}`). |
| `rebuno exec get <id>` | Show an execution's status and output. |
| `rebuno exec watch <id>` | Tail an execution's events until it finishes. |
| `rebuno exec events <id>` | Print the full event log with expanded payloads. |
| `rebuno exec cancel <id>` | Cancel a running execution. |

`exec ls` narrows with `--agent`, `--status`, and `--limit`.

```bash
rebuno exec create hello '{"query": "hello world"}'
  created 01a05eb7-e102-78e9 (pending); follow with 'rebuno exec watch 01a05eb7-e102-78e9'
rebuno exec watch 01a05eb7-e102-78e9
```

Quote the input so your shell keeps it in one piece. `exec watch` exits non-zero
if the execution failed or was cancelled, so it can gate a script.

## Approvals

| Command | Description |
|---------|-------------|
| `rebuno approval ls` | List approvals still pending. |
| `rebuno approval get <id>` | Show one approval. |
| `rebuno approval grant <id>` | Let the gated step proceed. |
| `rebuno approval deny <id>` | Refuse the gated step. |

Both decisions record who made them: `--by` names the approver and defaults to
`$USER`, and `--reason` records a rationale alongside it.

```bash
rebuno approval deny 01a05eb8-1c74-7f1e --reason "change freeze"
```

See [Policy](policy.md) for what puts a step in front of you.

## Policy

| Command | Description |
|---------|-------------|
| `rebuno policy test <bundle.yaml>` | Evaluate a bundle against test cases. |
| `rebuno policy set <agent-id> <bundle.yaml>` | Load or replace an agent's bundle. |

Both compile the bundle locally first, so one that does not parse is refused
before it reaches the kernel.

`policy test` runs the cases in the `.policytest.yaml` beside the bundle, and
exits non-zero if any expectation goes unmet.

| Flag | Description |
|------|-------------|
| `--cases <file>` | Take the cases from this file instead. |
| `--target <name>` | Probe one input rather than running cases, with `--args` and `--kind`. |
| `--execution <id>` | Replay a past execution's recorded steps, with `--agent-id`. Needs a running kernel. |

See [Testing a bundle](policy.md#testing-a-bundle).

## Ids

Listings print a shortened id. Any command taking an id accepts that, or any
other prefix of the full one.

```bash
rebuno exec ls
  ID                 AGENT   STATUS   AGE
  01a05eb6-cb55-7889 hello   running  4s

rebuno exec get 01a05eb6-cb55-7889
rebuno exec get 01a05eb6-cb55
```

A prefix that matches more than one execution is refused.
