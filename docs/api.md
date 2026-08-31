# HTTP API

All endpoints live under `/v0`. Bodies are JSON. Errors use a `{code, message,
details}` envelope.

## Authentication

- **Client and admin routes** use a bearer token: `Authorization: Bearer <token>`.
  Set it with `--bearer-token` / `REBUNO_BEARER_TOKEN` (required in server mode).
  The dev kernel disables auth.
- **Agent routes** use HMAC. The agent sends `Rebuno-Agent-Id` and
  `Rebuno-Signature: sha256=<HMAC-SHA256(secret, body)>`, computed over the raw
  request body with the agent's registered secret.
- A few routes (fetching execution input and steps) take either one: bearer for
  clients, HMAC for the agent.
- `/v0/health`, `/v0/ready`, and `/metrics` are unauthenticated.

---

## Client API

### Create an execution

`POST /v0/executions` · bearer

```json
{ "agent_id": "researcher", "input": { "query": "hello" }, "agent_version": "abc123" }
```

Returns `201` with the created execution. The kernel records `execution.created`
and enqueues a dispatch. `agent_version` is optional and opaque to the kernel.

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

### Stream live output

`GET /v0/executions/{id}/stream` · bearer

Server-Sent Events carrying the deltas an agent publishes while a step is still
running. Best-effort, never persisted, and separate from the event log. See
[Live streaming](streaming.md).

### Cancel an execution

`POST /v0/executions/{id}/cancel` · bearer → `204`

Records `execution.cancelled`, moves the execution to a terminal state, and stops
further dispatch.

---

## Agent API

The kernel dispatches a webhook carrying `{execution_id, dispatch_id}`. The agent
acks `200 OK`, then pulls what it needs and drives its effects. These routes are
HMAC-verified, except the reads, which also take a bearer token.

### Read execution input and steps

`GET /v0/executions/{id}` returns the original `input` and the current `status`.

`GET /v0/executions/{id}/steps?status=terminal` returns the execution's steps in
one read, for inspection and auditing. `status=terminal` trims the list to
`succeeded`, `failed`, and `denied`.

`GET /v0/executions/{id}/steps/{step_id}` looks up one step, and returns `404` if
it is not there.

### Submit a step

`POST /v0/executions/{id}/steps` · HMAC · header `Rebuno-Dispatch-Id: <id>`

```json
{ "kind": "tool_call", "target": "web_search", "args": {...}, "idempotency": "safe_to_retry" }
```

`kind` is `tool_call`, `llm_call`, or `local`. `Rebuno-Dispatch-Id` is required, and the
kernel rejects the request if it is missing, unknown, or belongs to another
execution. It is the `dispatch_id` from the webhook that delivered this attempt,
and it scopes the kernel's occurrence counter. The kernel derives the step ID,
runs the [replay short-circuit and policy](architecture.md), and returns a
decision. The `step_id` that comes back is what addresses the step's `complete`
and `fail` routes:

```json
{ "decision": "proceed", "step_id": "9a3f…" }
```

| `decision` | Meaning |
|------------|---------|
| `proceed` | New step allowed. Perform the effect, then call `complete`/`fail`. |
| `replay` | Already recorded. `result` or `error` comes back. Do not re-run the effect. |
| `denied` | Policy denied the call. `reason` says why. |
| `blocked` | Stop and exit the dispatch. The kernel re-dispatches later. `approval_id` is present when a human decision is pending. |
| `execution_blocked` | An earlier step is awaiting approval, so no new effect can start. Stop and exit the dispatch. |
| `rate_limited` | A rule's rate limit refused the call. `reason` says why. |
| `execution_terminal` | The execution is already cancelled or finished. Exit cleanly. |

### Report a step outcome

`POST /v0/executions/{id}/steps/{step_id}/complete` takes `{ "result": {...} }`
and records `step.succeeded`.

`POST /v0/executions/{id}/steps/{step_id}/fail` takes `{ "error": {...} }` and
records `step.failed`.

### Publish a stream delta

`POST /v0/executions/{id}/steps/{step_id}/stream` · HMAC → `204`

Body `{ "seq": <int64>, "data": "<chunk>" }`. Live output while a step runs,
delivered best-effort. `data` is opaque to the kernel and capped per batch. See
[Live streaming](streaming.md).

### Send a heartbeat

`POST /v0/executions/{id}/heartbeat` · HMAC → `204`

Touches the dispatch lease so a long-running dispatch is not reclaimed as
stalled.

### Report execution outcome

`POST /v0/executions/{id}/complete` takes `{ "output": {...} }` → `204`. Records
`execution.completed`.

`POST /v0/executions/{id}/fail` takes `{ "error": "..." }` → `204`. Records
`execution.failed`.

---

## Admin API

### Agents

`POST /v0/agents` · bearer registers or upserts an agent. Body
`{ "id", "webhook_url", "secret" }`. Returns `201`.

`GET /v0/agents` · bearer lists agents, with secrets redacted.

`GET /v0/agents/{id}` · bearer fetches one, with the secret redacted.

`DELETE /v0/agents/{id}` · bearer → `204`.

### Policy

`POST /v0/policies/{agent_id}` · bearer loads or replaces the agent's bundle.
Body `{ "bundle": "<raw YAML>" }` → `204`. See [Policy](policy.md).

### Approvals

`GET /v0/approvals` · bearer lists pending approvals.

`GET /v0/approvals/{id}` · bearer fetches one.

`POST /v0/approvals/{id}/grant` · bearer takes `{ "decided_by", "rationale?" }`
→ `204`. Resumes the execution, and the gated step proceeds.

`POST /v0/approvals/{id}/deny` · bearer takes `{ "decided_by", "rationale?" }`
→ `204`. The resumed loop sees a policy error at that step.

Both return `403 forbidden` when the approval lists `approvers` and `decided_by`
is not one of them. The kernel takes `decided_by` at face value, because the
bearer token carries no identity. The check stops someone off the list from
deciding. It cannot stop someone who types another person's name into the field.
See [Approvals](policy.md#approvals).

---

## Operational endpoints

`GET /v0/health` is a liveness check, returning `{"status":"ok"}`.

`GET /v0/ready` is a readiness check, returning `503` if a dependency check
fails.

`GET /metrics` serves Prometheus metrics.
