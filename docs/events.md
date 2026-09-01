# Events

Every state change in the kernel produces an immutable event. The event log is
the system of record. Executions, steps, and approvals are projections of it.

## Event structure

```json
{
  "execution_id": "0192f3a4-...",
  "event_seq": 7,
  "type": "step.succeeded",
  "payload": {
    "step_id": "…",
    "step_type": "llm_call",
    "target": "gpt-5.5",
    "usage": { "input_tokens": 812, "output_tokens": 96 }
  },
  "occurred_at": "2026-07-07T12:00:00Z"
}
```

- `event_seq` is monotonic per execution. It gives total order within one
  execution, not across executions.
- Events are append-only. Paging is by `after_seq`.
- Every `step.*` payload carries `step_id` and `step_type` (`tool_call`,
  `llm_call`, or `local`), so any event is self-describing in isolation.

## Execution events

The `execution.*` types cover the lifecycle of the run itself. Payloads carry
`execution_id` and `status`.

| Type | When |
|------|------|
| `execution.created` | Client created the execution. |
| `execution.started` | First dispatch acked; the agent is making progress. |
| `execution.blocked` | Paused on a human approval. |
| `execution.resumed` | An approval was granted, denied, or expired, and work continues. |
| `execution.completed` | Terminal, success. Payload carries the `output`. |
| `execution.failed` | Terminal, failure. Payload carries a `reason`. |
| `execution.cancelled` | Terminal, cancelled by the client. |

## Step events

The `step.*` types cover the lifecycle of each effect, whether a tool call, an
LLM call, or a local step.

| Type | When |
|------|------|
| `step.proposed` | The agent submitted the step and the kernel has not decided yet. |
| `step.allowed` | Policy allowed it. Payload carries the matched `rule_id`. |
| `step.denied` | Policy denied it, or an approval was denied or expired. |
| `step.awaiting_approval` | Policy requires a human decision, and an approval was created. |
| `step.rate_limited` | A rule's rate limit refused or parked it. No step row is written. |
| `step.executing` | Written before the external call runs, as the durable intent to act. |
| `step.succeeded` | Terminal. An `llm_call` also carries `usage` when token counts were found in the response. |
| `step.failed` | Terminal, with the recorded `error`. |
| `step.cancelled` | Terminal. The execution was cancelled while the step was in flight, so whether the effect ran is unknown. |

Payloads stay lean. They identify the step (`step_id`, `step_type`, and `target`
where it is known) and carry decision context (`rule_id`, `error`). Results are
not in the payload. The result of a tool call, and the request and response bodies
of an `llm_call`, live on the step itself, so fetch them with
`GET /v0/executions/{id}/steps/{step_id}`.

## Approval events

The `approval.*` types cover the lifecycle of each human-in-the-loop approval.
Payloads carry `approval_id`, `step_id`, `execution_id`, and `status`.

| Type | When |
|------|------|
| `approval.requested` | An approval was created. |
| `approval.granted` | A human granted it, with `decided_by` and `rationale`. |
| `approval.denied` | A human denied it. |
| `approval.expired` | The approval's timeout elapsed. The gated step is denied and the execution resumes. |

## Dispatch events

The `dispatch.*` types cover webhook delivery. Payloads carry `dispatch_id`,
`execution_id`, `status`, and `attempt`, which counts the times the kernel has
claimed this dispatch and handed the execution to an agent. It is `0` until the
first claim.

| Type | When |
|------|------|
| `dispatch.queued` | A dispatch was created and enqueued. |
| `dispatch.acked` | A delivery attempt returned `200 OK`. |
| `dispatch.failed` | A delivery attempt failed. Emitted on every attempt, including the one that reaches `max_attempts`. |
| `dispatch.discarded` | A claimed dispatch was dropped undelivered; its execution was already terminal. |
