# Events

Every state change in the kernel produces an immutable event. The event log is
the system of record — executions, steps, and approvals are all projections of
it. Read events over the API (`GET /v0/executions/{id}/events`) or in the REPL
(`exec events <id>`, `exec watch <id>`).

## Event structure

```json
{
  "execution_id": "0192f3a4-...",
  "event_seq": 7,
  "type": "step.succeeded",
  "payload": { "step_id": "…", "step_type": "tool_call", "result": {...} },
  "occurred_at": "2026-07-07T12:00:00Z"
}
```

- `event_seq` is monotonic **per execution** — it gives total order within an
  execution, not across them.
- Events are append-only. Paging is by `after_seq`.
- Every `step.*` payload carries `step_id` and `step_type` (`tool_call` or
  `llm_call`) so any event is self-describing in isolation.

The taxonomy is **closed**: only the four categories below are recordable.

## `execution.*`

The lifecycle of the run itself.

| Type | When |
|------|------|
| `execution.created` | Client created the execution. Records the input and `agent_version`. |
| `execution.started` | First dispatch acked; the agent is making progress. |
| `execution.blocked` | Paused on a human approval. |
| `execution.resumed` | An approval resolved; work continues. |
| `execution.completed` | Terminal, success. Payload carries the `output`. |
| `execution.failed` | Terminal, failure. Payload carries a `reason`. |
| `execution.cancelled` | Terminal, cancelled by the client. |

## `step.*`

The lifecycle of each effect (tool call or LLM call).

| Type | When |
|------|------|
| `step.proposed` | The agent submitted the step; the kernel hasn't decided yet. |
| `step.allowed` | Policy allowed it. Payload carries the matched `rule_id`. |
| `step.denied` | Policy denied it (or an approval was denied/expired). |
| `step.awaiting_approval` | Policy requires a human decision; an approval was created. |
| `step.executing` | Written **before** the external call runs — the durable "intent to act". |
| `step.succeeded` | Terminal, with the recorded `result`. |
| `step.failed` | Terminal, with the recorded `error`. |
| `step.cancelled` | The step was cancelled. |

For `llm_call` steps, `step.executing` carries the request input (model, messages,
tools, params) and `step.succeeded` carries the output (message, tool calls, token
counts, cost).

## `approval.*`

The lifecycle of each human-in-the-loop approval.

| Type | When |
|------|------|
| `approval.requested` | An approval was created. Payload carries `approval_id`. |
| `approval.granted` | A human granted it (with `decided_by`, `rationale`). |
| `approval.denied` | A human denied it. |
| `approval.expired` | The approval's timeout elapsed. |

## `dispatch.*`

Webhook delivery attempts.

| Type | When |
|------|------|
| `dispatch.sent` | A dispatch was enqueued. |
| `dispatch.acked` | The agent acked the webhook with `200 OK`. |
| `dispatch.failed` | A delivery attempt failed. |
| `dispatch.retried` | A failed dispatch was re-claimed for another attempt. |
| `dispatch.exhausted` | Max attempts reached; the execution fails with `dispatch_exhausted`. |
