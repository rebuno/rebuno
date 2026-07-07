# CLI

The `rebuno` binary is the kernel. It has three commands:

```bash
rebuno dev [--config manifest.yaml]     # in-memory dev kernel (no auth, no deps)
rebuno server --db-url … --bearer-token …   # production kernel (Postgres)
rebuno version
```

Build it with `make build` (outputs `bin/rebuno`), or run from source with
`go run ./cmd/rebuno …`.

## The REPL

When `rebuno dev` (or `server`) runs with a terminal on stdin, it starts an
interactive REPL that drives the kernel in-process — it shares the same store as
the running HTTP server, so executions created here are dispatched normally. With
no TTY (piped input, CI, Docker without `-t`) the kernel just serves HTTP.

```
rebuno> help
```

| Command | Description |
|---------|-------------|
| `agent ls` | List registered agents. |
| `agent get <id>` | Show an agent and its policy bundle. |
| `agent add <config.yaml>` | Register agent(s) from a provisioning manifest. |
| `agent rm <id>` | Delete an agent. |
| `exec ls` | List executions, newest first. |
| `exec create <agent> [json]` | Start an execution (input defaults to `{}`). |
| `exec get <id>` | Show an execution's status and output. |
| `exec watch <id>` | Tail an execution's events until it finishes. |
| `exec events <id>` | Print the full event log with expanded payloads. |
| `exec cancel <id>` | Cancel a running execution. |
| `quit` | Stop the kernel and exit. |

IDs accept a unique short-id prefix (the 8-char form shown by `exec ls`), so you
can type back the IDs you see.

```
rebuno> exec create hello {"query": "hello world"}
  created a1b2c3d4 (pending) — 'exec watch a1b2c3d4' to follow
rebuno> exec watch a1b2c3d4
```

Command history persists to `~/.rebuno_repl_history`.
