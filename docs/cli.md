# rebuno CLI

The `rebuno` binary includes built-in commands for operational visibility into the kernel. These inspection commands communicate through the kernel's HTTP API, making them a terminal-based counterpart to the Explorer web UI.

## Installation

Build from source:

```bash
go build -o bin/rebuno ./cmd/rebuno
```

This produces a `rebuno` binary in the bin directory — a single binary that includes the kernel (`rebuno dev`, `rebuno server`) and all inspection commands. Alternatively, run `make build` to output to `bin/rebuno`.

## Connection

All subcommands connect to the kernel's HTTP API. Provide the URL and optional API key via flags or environment variables:

```bash
# Via flags
rebuno --url http://localhost:8080 --api-key "my-token" executions

# Via environment variables
export REBUNO_KERNEL_URL="http://localhost:8080"
export REBUNO_API_KEY="my-token"
rebuno executions
```

| Flag | Env Variable | Default | Description |
|------|-------------|---------|-------------|
| `--url` | `REBUNO_KERNEL_URL` | `http://localhost:8080` | Kernel HTTP URL |
| `--api-key` | `REBUNO_API_KEY` | _(none)_ | Bearer token for authentication |

## Subcommands

### health

Check connectivity to the kernel.

```bash
rebuno health
```

### executions

List executions (most recent first).

```bash
rebuno executions
rebuno executions --status running
rebuno executions --agent researcher
rebuno executions --status failed --agent researcher --limit 10
```

| Flag | Default | Description |
|------|---------|-------------|
| `--status` | | Filter by execution status (`pending`, `running`, `blocked`, `completed`, `failed`, `cancelled`) |
| `--agent` | | Filter by agent ID |
| `--limit` | `50` | Maximum number of results |

Output columns: `ID`, `STATUS`, `AGENT`, `CREATED`, `UPDATED`.

### execution

Show details for a single execution.

```bash
rebuno execution exec-abc123
```

Output fields: `ID`, `Status`, `Agent`, `Labels`, `Created`, `Updated`, `Input`, `Output`.

### events

Show the event log for an execution. Use `--tail` to follow live events via SSE.

```bash
rebuno events exec-abc123
rebuno events exec-abc123 --limit 20
rebuno events exec-abc123 --tail
```

| Flag | Default | Description |
|------|---------|-------------|
| `--tail` | `false` | Follow live events via server-sent events (exits on terminal events or Ctrl+C) |
| `--limit` | `100` | Maximum number of events (ignored with `--tail`) |

Each event is printed as:

```
[sequence] timestamp event_type step=step_id payload_json
```

### create

Create a new execution.

```bash
rebuno create --agent researcher
rebuno create --agent researcher --input '{"query": "hello"}'
rebuno create --agent researcher --label env=prod --label team=search
```

| Flag | Description |
|------|-------------|
| `--agent` | Agent ID (required) |
| `--input` | Input as a JSON string |
| `--label` | Labels as `key=value` (repeatable) |

### cancel

Cancel a running execution.

```bash
rebuno cancel exec-abc123
```

### signal

Send a signal to an execution.

```bash
rebuno signal exec-abc123 --type approve
rebuno signal exec-abc123 --type feedback --payload '{"message": "try again"}'
```

| Flag | Description |
|------|-------------|
| `--type` | Signal type (required) |
| `--payload` | Signal payload as a JSON string |

## Color Output

Status values are color-coded in terminal output: pending (yellow), running (cyan), blocked (magenta), completed (green), failed (red), cancelled (gray). Set the `NO_COLOR` environment variable to disable colors.
