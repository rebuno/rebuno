# HTTP API

All endpoints live under `/v0`. Bodies are JSON. Errors use a `{code, message,
details}` envelope.

## Authentication

- **Client & admin routes** use a bearer token: `Authorization: Bearer <token>`.
  Set it with `--bearer-token` / `REBUNO_BEARER_TOKEN` (required in server mode).
  The dev kernel disables auth.
- **Agent routes** use HMAC. The agent sends `Rebuno-Agent-Id` and
  `Rebuno-Signature: sha256=<HMAC-SHA256(secret, body)>`, computed over the raw
  request body with the agent's registered secret.
- A few routes (fetching execution input/steps) accept **either** — bearer for
  clients, HMAC for the agent.
- `/v0/health`, `/v0/ready`, and `/metrics` are unauthenticated.

---

## Client API

### Create an execution

`POST /v0/executions` · bearer

```json
{ "agent_id": "researcher", "input": { "query": "hello" }, "agent_version": "abc123" }
```

Returns `201` with the created execution (records `execution.created` and enqueues
a dispatch). `agent_version` is optional and opaque to the kernel.

### Get an execution

`GET /v0/executions/{id}` · bearer or HMAC

```json
{
  "id": "0192...", "agent_id": "researcher", "status": "completed",
  "input": {...}, "output": {...}, "failure_reason": "",
  "created_at": "...", "updated_at": "...", "deadline_at": null
}
```

### List executions

`GET /v0/executions?agent_id=&status=&cursor=&limit=` · bearer

Newest first. Keyset paging: pass the returned `next_cursor` back as `cursor`.

```json
{ "executions": [ ... ], "next_cursor": "0192..." }
```

### Get the event log

`GET /v0/executions/{id}/events?after_seq=&limit=` · bearer

Returns an ordered array of [events](events.md). `limit` defaults to 100, max
1000. Poll with `after_seq` set to the last `event_seq` you saw.

### Cancel an execution

`POST /v0/executions/{id}/cancel` · bearer → `204`

Records `execution.cancelled`, transitions to terminal, stops further dispatch.

---

## Agent API

The kernel dispatches a webhook carrying `{execution_id, dispatch_id}`; the agent
acks `200 OK`, then pulls what it needs and drives its effects. These routes are
HMAC-verified (except the reads, which also accept bearer).

### Fetch input / steps

`GET /v0/executions/{id}` — original `input` and current `status`.

`GET /v0/executions/{id}/steps?status=terminal` — the execution's steps in one
read, so the agent builds its `{step_id → result}` map at dispatch start.
`status=terminal` trims to `succeeded`/`failed`/`denied`.

`GET /v0/executions/{id}/steps/{step_id}` — point lookup of one step; `404` if
absent.

### Submit a step

`POST /v0/executions/{id}/steps` · HMAC · header `Rebuno-Step-Id: <id>`

```json
{ "kind": "tool_call", "target": "web_search", "args": {...}, "idempotency": "safe_to_retry" }
```

`kind` is `tool_call` or `llm_call`. The kernel recomputes the step ID and
compares it to the header, runs the [replay short-circuit and policy](architecture.md),
and returns a **decision**:

```json
{ "decision": "proceed" }
```

| `decision` | Meaning |
|------------|---------|
| `proceed` | New step allowed — perform the effect, then call `complete`/`fail`. |
| `replay` | Already recorded — `result` or `error` is returned; do **not** re-run. |
| `denied` | Policy denied it; `reason` explains. |
| `blocked` | Awaiting human approval; `approval_id` is returned. |
| `execution_terminal` | The execution is cancelled/done; exit cleanly. |

### Report a step outcome

`POST /v0/executions/{id}/steps/{step_id}/complete` — body `{ "result": {...} }`.
Records `step.succeeded`.

`POST /v0/executions/{id}/steps/{step_id}/fail` — body `{ "error": {...} }`.
Records `step.failed`.

### Report execution outcome

`POST /v0/executions/{id}/complete` — body `{ "output": {...} }` → `204`. Records
`execution.completed`.

`POST /v0/executions/{id}/fail` — body `{ "error": "..." }` → `204`. Records
`execution.failed`.

---

## Admin API

### Agents

`POST /v0/agents` · bearer — register/upsert. Body `{ "id", "webhook_url",
"secret" }`. Returns `201`.

`GET /v0/agents` · bearer — list (secrets redacted).

`GET /v0/agents/{id}` · bearer — fetch one (secret redacted).

`DELETE /v0/agents/{id}` · bearer → `204`.

### Policy

`POST /v0/policies/{agent_id}` · bearer — load/replace the agent's bundle. Body
`{ "bundle": "<raw YAML>" }` → `204`. See [policy.md](policy.md).

### Approvals

`GET /v0/approvals` · bearer — list pending approvals.

`GET /v0/approvals/{id}` · bearer — fetch one.

`POST /v0/approvals/{id}/grant` · bearer — body `{ "decided_by", "rationale?" }`
→ `204`. Resumes the execution; the gated step proceeds.

`POST /v0/approvals/{id}/deny` · bearer — body `{ "decided_by", "rationale?" }`
→ `204`. The resumed loop sees a policy error at that step.

---

## Operational

`GET /v0/health` — liveness (`{"status":"ok"}`).

`GET /v0/ready` — readiness; `503` if a dependency check fails.

`GET /metrics` — Prometheus metrics.
