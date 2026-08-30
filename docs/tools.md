# Tools and effects

An agent's work is a sequence of effects: tool calls, LLM calls, and local steps.
Every effect becomes a step. It gets a deterministic ID from its content, goes
through policy, and is recorded in the event log. A step that already has a
recorded result returns it instead of running again.

To the kernel all three travel one submission path, distinguished only by `kind`
(`tool_call`, `llm_call`, or `local`). Recording an `llm_call` takes an
interceptor, since the request is made inside a provider SDK. See
[LLM calls](llm-calls.md).

## An effect becomes a step

When the agent is about to perform an effect it submits a step
(`POST /v0/executions/{id}/steps`) with the `kind`, the `target` (the tool name or
model id), and the arguments. The kernel:

1. Computes the step's deterministic ID.
2. Looks it up in the `steps` projection. If it already succeeded or failed, the
   kernel returns `replay` with the recorded outcome.
3. Otherwise evaluates [policy](policy.md) and records the decision, returning
   `proceed`, `denied`, `rate_limited`, or `blocked`.

On `proceed` the agent runs the effect and reports the result
(`.../complete`) or error (`.../fail`). See the [HTTP API](api.md#agent-api) for
the shapes.

## Step identity

A step's ID is derived from the effect's content:

```
step_id = hash(execution_id, kind, target, args_hash, occurrence)
```

- `target` is the tool name or model id.
- `args_hash` is a stable hash of the canonicalized arguments, meaning canonical
  JSON for tools and the canonical request body for LLM calls.
- `occurrence` is the count of prior identical calls in this run of the agent, so
  the same call with the same arguments twice yields two distinct steps. It
  restarts at zero on every dispatch, which is what lets a re-run reproduce the
  same IDs.

The kernel assigns the ID and returns it in the decision; the agent submits only
`{kind, target, args}` and its `dispatch_id`. Because IDs are content-derived,
parallel effects and reordering across replays are safe. The same set of calls
always produces the same set of IDs. See
[Step identity](architecture.md#step-identity).

## Idempotency modes

If an agent crashes after a step started but before its result was recorded, the
step is orphaned, and the kernel cannot tell whether the side effect happened.
Each effect declares how to recover:

| Mode | On an orphaned step |
|------|--------------------|
| `safe_to_retry` (default) | Re-run the effect. Right for reads and naturally idempotent operations. |
| `at_most_once` | Do not re-run. The kernel marks the step failed with reason `indeterminate` and the agent decides how to reconcile. Use for non-idempotent destructive operations. |

A step that fails with `indeterminate` might still have had a side effect. Treat
it as a failure that may have run, and re-check external state before retrying.

## Local steps

A `local` step records a value the agent produced itself, such as the current
time, a random choice, or a fresh id. Record it when the value decides which
effects run next, so a resumed run replays the same value. Both SDKs expose it as
`step(name, fn)`.

An unmatched `local` step is allowed whatever `default_action` says, so a
deny-by-default bundle will not block one. See [Evaluation](policy.md#evaluation).
