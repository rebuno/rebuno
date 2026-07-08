# Tools & effects

An agent's work is a sequence of **effects**: tool calls and LLM calls. Every
effect becomes a **step** — it goes through policy, gets a deterministic ID, and is
recorded in the event log. On replay, an effect that already has a recorded result
returns it instead of running again.

To the kernel a tool call and an LLM call are the same kind of thing,
distinguished only by `kind` (`tool_call` or `llm_call`). Both travel the same
submission path and obey the same replay, idempotency, and policy rules.

## An effect becomes a step

When the agent is about to perform an effect it submits a step
(`POST /v0/executions/{id}/steps`) with the `kind`, the `target` (the tool name or
model id), and the arguments. The kernel:

1. Computes the step's deterministic ID and validates the one the agent sent.
2. Looks it up in the `steps` projection — if it already succeeded or failed, it
   returns `replay` with the recorded outcome.
3. Otherwise evaluates [policy](policy.md) and records the decision, returning
   `proceed`, `denied`, or `blocked`.

On `proceed` the agent runs the effect and reports the result
(`.../complete`) or error (`.../fail`). See the [HTTP API](api.md#agent-api) for
the shapes.

## Step identity

A step's ID is derived from the effect's **content**, not its position in the run:

```
step_id = hash(execution_id, kind, target, args_hash, occurrence)
```

- `target` — the tool name or model id.
- `args_hash` — a stable hash of the canonicalized arguments (canonical JSON for
  tools; the canonical request body for LLM calls).
- `occurrence` — the count of prior identical calls in this execution, so the same
  call with the same arguments twice yields two distinct steps.

Because IDs are content-derived, parallel effects and reordering across replays are
safe — the same set of calls always produces the same set of IDs. See
[architecture.md](architecture.md#step-identity).

## Idempotency modes

If an agent crashes after a step started but before its result was recorded, the
step is *orphaned* — the kernel can't tell whether the side effect happened. Each
effect declares how to recover:

| Mode | On an orphaned step |
|------|--------------------|
| `safe_to_retry` (default) | Re-run the effect, passing a step-ID-derived idempotency key. Right for reads, naturally-idempotent operations, or providers that honor idempotency keys. |
| `at_most_once` | Do **not** re-run. The kernel marks the step failed with reason `indeterminate` and the agent decides how to reconcile. Use for non-idempotent destructive operations where even a deduplicated retry is unacceptable. |

A step that fails with `indeterminate` **may still have had a side effect** — treat
it as a failure that might have run, and re-check external state before retrying.

LLM calls are always `safe_to_retry`: the step ID doubles as the provider's
idempotency key.

## LLM calls

An LLM call is an `llm_call` step submitted the same way, with the model as
`target` and the request body as the arguments. Its identity is computed over a
canonical form of the request (messages, tools, model, sampling params) and
excludes operational noise (request IDs, trace headers, streaming flag). A recorded
LLM call replays from the log — **no provider call ever happens on replay** — and
its `step.succeeded` event carries the response, token counts, and cost. See
[events.md](events.md#step) and [policy.md](policy.md).
