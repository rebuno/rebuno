# CLI

The `rebuno` binary is the kernel. It has three commands:

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

## The dev REPL

`rebuno dev` starts an interactive REPL when stdin is a terminal.

```
rebuno> help
```

| Command | Description |
|---------|-------------|
| `agent ls` | List registered agents. |
| `agent get <id>` | Show an agent and its policy bundle. |
| `agent add <config.yaml>` | Register one or more agents from a provisioning manifest. |
| `agent rm <id>` | Delete an agent. |
| `exec ls` | List executions, newest first. |
| `exec create <agent> [json]` | Start an execution (input defaults to `{}`). |
| `exec get <id>` | Show an execution's status and output. |
| `exec watch <id>` | Tail an execution's events until it finishes. |
| `exec events <id>` | Print the full event log with expanded payloads. |
| `exec cancel <id>` | Cancel a running execution. |
| `quit` | Stop the kernel and exit. |

IDs accept a unique short-id prefix, so you can type back the 8-character form
that `exec ls` prints.

```
rebuno> exec create hello {"query": "hello world"}
  created a1b2c3d4 (pending) — 'exec watch a1b2c3d4' to follow
rebuno> exec watch a1b2c3d4
```

Command history persists to `~/.rebuno_repl_history`.
