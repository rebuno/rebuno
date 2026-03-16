# Architecture

## Core Concepts

**Execution** — A unit of work. Created with an agent ID and input, it progresses through states: `pending -> running <-> blocked -> completed/failed/cancelled`.

**Agent** — A process that receives executions via SSE, invokes tools via intents, and reports results. Identified by `agent_id`.

**Intent** — A proposal from an agent. The kernel validates it against policy and either accepts or denies it. Types: `invoke_tool`, `complete`, `fail`, `wait`.

**Step** — Created when a tool invocation intent is accepted. Tracks the tool call through dispatch, execution, and result.

**Policy** — Declarative YAML rules that control which tools agents can invoke. Evaluated on every `tool.invoke` intent. See [Policy docs](policy.md).

**Runner** — A process that executes tools remotely. Connects via SSE, declares capabilities, and receives jobs from the kernel.

## Typical Workflow

```
1. GET  /v0/agents/stream       — agent opens SSE connection
2. GET  /v0/runners/stream      — runner opens SSE connection (registers capabilities)
3. POST /v0/executions          — create an execution
   -> kernel pushes execution.assigned via SSE to agent
4. POST /v0/agents/intent       — agent submits invoke_tool intent
   -> policy evaluates -> step created
   -> if remote: kernel pushes job.assigned via SSE to runner
5. POST /v0/agents/step-result  — agent reports local tool result
   (or runner submits via POST /v0/runners/{id}/results)
6. POST /v0/agents/intent       — agent submits complete intent
```

## State Transitions

### Execution States

```
pending --> running <--> blocked --> completed
  |           |             |
  |           |             +--> failed
  |           |             +--> cancelled
  |           |
  |           +--> completed
  |           +--> failed
  |           +--> cancelled
  |           +--> pending  (on agent disconnect)
  |
  +--> cancelled
```

| From | To | Trigger |
|------|-----|---------|
| `pending` | `running` | Kernel assigns execution to an agent via SSE |
| `pending` | `cancelled` | Cancel request |
| `running` | `blocked` | Agent submits `invoke_tool` or `wait` intent |
| `running` | `completed` | Agent submits `complete` intent |
| `running` | `failed` | Agent submits `fail` intent, or execution timeout |
| `running` | `cancelled` | Cancel request |
| `running` | `pending` | Agent SSE connection drops (execution returned to queue) |
| `blocked` | `running` | Tool step completes or signal received |
| `blocked` | `failed` | Step fails, execution timeout, or agent timeout |
| `blocked` | `cancelled` | Cancel request |

### Step States

```
pending --> dispatched --> running --> succeeded
  |             |            |
  |             |            +--> failed
  |             |            +--> timed_out
  |             |            +--> cancelled
  |             |
  |             +--> timed_out
  |             +--> cancelled
  |
  +--> cancelled
  +--> timed_out
```

| From | To | Trigger |
|------|-----|---------|
| `pending` | `dispatched` | Kernel dispatches step to a runner |
| `pending` | `cancelled` | Execution cancelled |
| `pending` | `timed_out` | Step deadline reached |
| `dispatched` | `running` | Runner reports step started |
| `dispatched` | `timed_out` | Step deadline reached |
| `dispatched` | `cancelled` | Execution cancelled |
| `running` | `succeeded` | Runner or agent reports success |
| `running` | `failed` | Runner or agent reports failure |
| `running` | `timed_out` | Step deadline reached |
| `running` | `cancelled` | Execution cancelled |

When a step fails with `retryable: true`, the kernel emits a `step.retried` event and creates a new step attempt.
