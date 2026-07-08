# Architecture

Rebuno is a **kernel-authoritative** runtime. A stateless Go kernel owns an
append-only event log in Postgres and is the sole writer of state. Agents are
stateless HTTP services the kernel dispatches to over signed webhooks. Every
effect an agent performs — a tool call or an LLM call — becomes a durably
recorded *step*, and re-running an agent short-circuits any step that already
has a recorded result.

```
client ──create──▶ kernel ──webhook──▶ agent
                     │                    │
                     │◀── submit_step ────┘   (each tool/LLM call)
                     ▼
                  Postgres
              (events + steps)
```

## The six entities

| Entity | What it is |
|--------|-----------|
| **Execution** | One run of an agent against an input. Identified by a UUIDv7. Holds a status and, when terminal, an output. |
| **Step** | A single effect within an execution — a `tool_call` or an `llm_call`. Identified by a deterministic content hash. |
| **Event** | An immutable record of something that happened. Ordered by `(execution_id, event_seq)`. The event log is the system of record; everything else is a projection. |
| **Approval** | A pending human decision that gates a step. Has a timeout and a resolution. |
| **Agent** | A stateless HTTP service registered by name, with a webhook URL and an HMAC secret. |
| **Policy** | A per-agent YAML rule bundle, evaluated when a step is submitted. |

## State machines

**Execution:**

```
pending → running ↔ blocked → completed
                            ↘ failed
                            ↘ cancelled
```

`blocked` means the execution is paused waiting on a human approval; it re-enters
`running` when the approval resolves.

**Step:**

```
proposed → allowed → executing → succeeded
        ↘ denied                ↘ failed
        ↘ awaiting_approval → (approved) → executing → …
```

## Determinism and replay

When an execution resumes — after a crash, an approval, or any pause — the kernel
re-dispatches the agent. The agent runs again from its entry point with the same
input. Every effect is intercepted before it runs:

1. A deterministic **step ID** is computed from the call's content.
2. It is looked up against the kernel's `steps` projection (a point query).
3. If a result is recorded, it is returned and **the effect does not run**.
4. If not, the effect runs and its outcome is recorded against that same ID.

Replay cost is **one indexed lookup per effect**, independent of how many events
the execution has accumulated. Replay never re-invokes an LLM, re-runs a tool, or
re-calls an external service.

Rebuno records *effects*, not *conversations*. It does not reconstruct framework
state or conversation history — the agent author reloads those from wherever they
store them.

### Step identity

```
step_id = hash(execution_id, kind, target, args_hash, occurrence)
```

- `kind` — `tool_call` or `llm_call`.
- `target` — the tool name or model id.
- `args_hash` — a stable hash of the canonicalized arguments.
- `occurrence` — the count of prior identical calls in this execution, so calling
  `read_file("foo")` twice yields two distinct step IDs.

Content-derived IDs are stable under reordering and parallel dispatch: two replays
that issue the same set of effects produce the same step IDs regardless of order.
The agent computes the ID and sends it in the `Rebuno-Step-Id` header; the kernel
recomputes it and rejects a mismatch — agreement is the contract, not trust.

## Durability and failure

Every effect is a three-act sequence: `step.executing` is written **before** the
external call; the terminal event (`step.succeeded` / `step.failed`) is written
**after**. This ordering lets the kernel detect orphaned effects on recovery.

On re-dispatch the kernel finds each step in one of three states:

- **Absent** → run it.
- **Terminal** → replay the recorded outcome; never re-invoke.
- **Started only (orphan)** → resolve by the step's declared idempotency:
  - `safe_to_retry` (default) — re-invoke with a step-ID-derived idempotency key.
  - `at_most_once` — mark the step failed with `indeterminate`; the agent's loop
    decides how to reconcile.

The kernel guarantees: `step.executing` before any external effect, the terminal
event as the source of truth (never overridden), and state transitions atomic with
the event that caused them. It does **not** guarantee exactly-once external side
effects — that is delegated to provider idempotency keys.

## Dispatch and delivery

The kernel enqueues a dispatch (a row in the `dispatches` table) in the same
transaction as the event that triggers it. A background loop on every replica
claims due work with `SELECT … FOR UPDATE SKIP LOCKED` and POSTs to the agent's
webhook:

```
POST <webhook_url>
Rebuno-Signature: sha256=<HMAC-SHA256(secret, body)>
{ "execution_id": "…", "dispatch_id": "…" }
```

The payload carries no history — the agent fetches what it needs from the API.
Delivery is **at-least-once**; the agent's handler must be safe under duplicate
dispatch, and step identity makes re-issued effects converge rather than
duplicate. Failed dispatches retry with exponential backoff; after exhaustion the
execution fails with `dispatch_exhausted`.

## Governance (policy)

Policy is evaluated once per effect, at step submission, for both tool and LLM
calls. A rule returns `allow`, `deny(reason)`, or `require_approval(config)`.
Policy is gate-keeping only — it never rewrites a request. See [policy.md](policy.md).

## Storage and HA

Postgres is the system of record. The `steps` table is a projection of the event
log, written **synchronously in the same transaction** as its events, so replay
lookups are read-after-write consistent and the projection never lags. Dev mode
substitutes an in-memory store with the same interfaces.

The HTTP API is stateless — any replica serves any request, and any replica
dispatches any execution (no connection registry, no sticky routing). Singleton
background work (approval-expiry, execution deadlines, cleanup, stale-dispatch
reaping) runs under Postgres advisory-lock leadership.

For the full mechanics — the write path, canonicalization rules, and per-failure
recovery behavior — see the rest of the docs: [agents](agents.md),
[tools](tools.md), [policy](policy.md), and [events](events.md).
