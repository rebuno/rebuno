# How it works

This page explains the machinery under `defineTool`, `rebunoFetch`, `step()`, and
resume. You don't need it to use the SDK, but it explains *why* the rules exist —
especially why handlers must be deterministic.

## The execution context

When the agent dispatches an execution, it builds an `ExecutionContext` and sets
it as an ambient value (via `node:async_hooks` `AsyncLocalStorage`) for the
duration of the handler. Every recording primitive finds it the same way — there's
no object to thread through your code:

- `defineTool` and `wrapTool` call `ctx.invokeTool(...)`
- `rebunoFetch` calls `ctx.invokeLlm(...)`
- `step()` calls `ctx.invokeTool(...)` (a step is a `tool_call`-kind step)

If any of these runs with no active context (outside a handler), it throws an
`Error`. You can also read the current context ambiently via the exported
`execution()` accessor, which returns the active `ExecutionContext` (and throws if
there isn't one).

## Determinism and step ids

Each effect is identified by a **step id** the SDK computes deterministically. On
every dispatch the handler re-runs, recomputes the same ids in the same order, and
the kernel matches each one to a recorded step.

The id is built from five fields:

```
stepId    = sha256( Σ `${byteLength(field)}:` + bytes(field) )   over the fields below
argsHash  = sha256( canonicalJson(args) )
fields    = [ executionId, kind, target, argsHash, occurrence ]
```

- **`kind`** — `tool_call` or `llm_call`.
- **`target`** — the tool id, or the model id for an LLM call.
- **`argsHash`** — a hash of the arguments, in *canonical JSON*.
- **`occurrence`** — a counter that disambiguates identical calls. The context
  counts how many times each `(kind, target, argsHash)` triple has appeared in
  this dispatch, so calling the same tool with the same args twice produces two
  distinct steps (occurrence 0, then 1).

**This is why handlers must be deterministic.** Replay works only if the second
run computes the same step ids in the same order as the first. If your handler
reads the clock, picks a random value, or generates an id *directly*, a resumed
run produces different arguments — a different `argsHash`, a different step id —
and the kernel rejects it with `409 step_id_divergence`, surfaced as
[`StepIDMismatch`](errors.md). Wrap that non-determinism in [`step()`](steps.md)
so the value is recorded once and replayed.

### Canonical JSON

`argsHash` and the wire encoding of step arguments use a canonical JSON that is
**byte-for-byte compatible with the Go kernel's encoding**, because the kernel
recomputes and validates the step id independently. Canonicalization means:
sorted object keys (by Unicode code point, which equals the kernel's UTF-8
byte-order sort), no whitespace, JSON-valid number literals, and Go-style string
escaping (`<`, `>`, `&`, `U+2028`, `U+2029` are escaped; everything else is raw
UTF-8). This lives in the SDK's `identity` module. A consequence: step arguments
must be JSON-canonicalizable (objects, arrays, strings, finite numbers, booleans,
null) — other values (non-finite numbers, functions, symbols) throw a
`TypeError`.

## Submitting and deciding a step

For each effect, the context asks the kernel what to do *before* running the body.
`submitStep` returns a `StepDecision`, one of:

| Decision | Meaning | SDK behavior |
|----------|---------|--------------|
| `proceed` | new step, run it | runs the body, then records the result |
| `replay` | already recorded | returns the stored result (or throws the stored error) — the body never runs |
| `denied` | policy denial | throws `PolicyError` |
| `rate_limited` | policy rate limit | throws `RateLimited` |
| `blocked` / `execution_blocked` | awaiting approval | throws `Blocked` — the dispatch unwinds and the webhook returns 200 |
| `execution_terminal` | execution is terminal | throws `Terminated` |

On `proceed`, the body runs; success calls `completeStep` with the result, failure
calls `failStep` and (for tools) throws `ToolError`. This is the single choke
point that gives every effect its policy, replay, and audit behavior.

## Replay hydration

Naively, replaying N recorded steps would be N kernel round trips. Instead, at the
start of a dispatch the agent calls `hydrate()`, which fetches all of the
execution's **terminal** steps in one read and builds a local `Map` keyed by step
id. The context's `decide` then resolves from that map first:

- **Map hit** (a terminal step) → replay locally, no round trip. The local
  decision is what the kernel would have returned: `succeeded` replays the result,
  `failed` replays the error, `denied` is a policy denial.
- **Map miss** → fall back to `submitStep`. A miss is *not* automatically "new" —
  the step might be non-terminal (still executing, or awaiting approval), so it
  must re-hit the kernel to run idempotency and approval logic. Only the kernel
  mints new steps.

If hydration fails, the SDK clears the replay map and falls back to per-step kernel
calls — correct, just chattier.

## Heartbeats and leases

A dispatch holds a lease so the kernel won't reclaim and re-dispatch it while it's
still running. Long effect bodies (LLM calls, slow tools) would outlive a fixed
lease, so while a body runs the context starts a timer (`setInterval`) that renews
the lease every ~30s via `heartbeat`.

The heartbeat only fires if the body **yields to the event loop** — i.e. it's
async and awaits something. All the naturally-long effects (provider calls, MCP
tools) are I/O-bound and async, so this holds. A fully synchronous, blocking body
starves the timer; offload CPU-bound work (e.g. to a worker thread) so the loop
stays live. This is the reason for the "offload blocking work" guidance in
[Tools](tools.md).

## Signing

Both directions of the agent↔kernel channel are authenticated with the shared
agent secret, using HMAC-SHA256 via Web Crypto (`crypto.subtle`):

- **Kernel → agent (webhook):** the kernel signs the request body; the agent
  verifies `Rebuno-Signature: sha256=...` with a constant-time compare before
  doing anything. Bad or missing signature → `401`.
- **Agent → kernel:** the agent's kernel client signs every request body the same
  way and sends `Rebuno-Signature` plus `Rebuno-Agent-Id`. Step submissions also
  send the SDK-computed id in `Rebuno-Step-Id` for the kernel to validate.

The separate [`Client`](client.md) uses Bearer auth (`REBUNO_API_KEY`) instead —
it's a client/admin caller, not the agent.
