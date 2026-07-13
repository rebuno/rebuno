# How it works

This page explains the machinery under `@tool`, `http_client()`, `step()`, and
resume. You don't need it to use the SDK, but it explains *why* the rules exist —
especially why handlers must be deterministic.

## The execution context

When the agent dispatches an execution, it builds an `ExecutionContext` and sets
it as an ambient value (a `contextvars.ContextVar`) for the duration of the
handler. Every recording primitive finds it the same way — there's no object to
thread through your code:

- `@tool` and `wrap_tool` call `ctx.invoke_tool(...)`
- `http_client()` calls `ctx.invoke_llm(...)`
- `step()` calls `ctx.invoke_tool(...)` (a step is a `tool_call`-kind step)

If any of these runs with no active context (outside a handler), it raises
`RuntimeError`. You can also read the current context ambiently via
`rebuno.execution`, which proxies to the active `ExecutionContext`.

## Determinism and step ids

Each effect is identified by a **step id** the SDK computes deterministically. On
every dispatch the handler re-runs, recomputes the same ids in the same order,
and the kernel matches each one to a recorded step.

The id is built from five fields:

```
step_id = sha256( length_prefixed(execution_id, kind, target, args_hash, occurrence) )
args_hash = sha256( canonical_json(args) )
```

- **`kind`** — `tool_call` or `llm_call`.
- **`target`** — the tool id, or the model id for an LLM call.
- **`args_hash`** — a hash of the arguments, in *canonical JSON*.
- **`occurrence`** — a counter that disambiguates identical calls. The context
  counts how many times each `(kind, target, args_hash)` triple has appeared in
  this dispatch, so calling the same tool with the same args twice produces two
  distinct steps (occurrence 0, then 1).

**This is why handlers must be deterministic.** Replay works only if the second
run computes the same step ids in the same order as the first. If your handler
reads the clock, picks a random value, or generates an id *directly*, a resumed
run produces different arguments — a different `args_hash`, a different step id —
and the kernel rejects it with `409 step_id_divergence`, surfaced as
[`StepIDMismatch`](errors.md). Wrap that non-determinism in
[`rebuno.step()`](steps.md) so the value is recorded once and replayed.

### Canonical JSON

`args_hash` and the wire encoding of step arguments use a canonical JSON that is
**byte-for-byte compatible with the Go kernel's encoding**, because the kernel
recomputes and validates the step id independently. Canonicalization means:
sorted object keys (UTF-8 byte order, which equals Python code-point order), no
whitespace, preserved number literals, and Go-style string escaping (`<`, `>`,
`&`, `U+2028`, `U+2029` are escaped; everything else is raw UTF-8). This lives in
`rebuno.identity`. A consequence: step arguments must be JSON-canonicalizable
(dicts, lists, strings, numbers, bools, null) — other types raise `TypeError`.

## Submitting and deciding a step

For each effect, the context asks the kernel what to do *before* running the
body. `submit_step` returns a `StepDecision`, one of:

| Decision | Meaning | SDK behavior |
|----------|---------|--------------|
| `proceed` | new step, run it | runs the body, then records the result |
| `replay` | already recorded | returns the stored result (or raises the stored error) — the body never runs |
| `denied` | policy denial | raises `PolicyError` |
| `rate_limited` | policy rate limit | raises `RateLimited` |
| `blocked` / `execution_blocked` | awaiting approval | raises `Blocked` — the dispatch unwinds and the webhook returns 200 |
| `execution_terminal` | execution is terminal | raises `Terminated` |

On `proceed`, the body runs; success calls `complete_step` with the result,
failure calls `fail_step` and (for tools) raises `ToolError`. This is the single
choke point that gives every effect its policy, replay, and audit behavior.

## Replay hydration

Naively, replaying N recorded steps would be N kernel round trips. Instead, at
the start of a dispatch the agent calls `hydrate()`, which fetches all of the
execution's **terminal** steps in one read and builds a local map keyed by step
id. `_decide` then resolves from that map first:

- **Map hit** (a terminal step) → replay locally, no round trip. The local
  decision is byte-for-byte what the kernel would have returned: `succeeded`
  replays the result, `failed` replays the error, `denied` is a policy denial.
- **Map miss** → fall back to `submit_step`. A miss is *not* automatically "new" —
  the step might be non-terminal (an orphan still `executing`, or
  `awaiting_approval`), so it must re-hit the kernel to run idempotency and
  approval logic. Only the kernel mints new steps.

If hydration fails, the SDK logs a warning and falls back to per-step kernel
calls — correct, just chattier.

## Heartbeats and leases

A dispatch holds a lease so the kernel won't reclaim and re-dispatch it while
it's still running. Long effect bodies (LLM calls, slow tools) would outlive a
fixed lease, so while a body runs the context spawns a background task that
renews the lease every ~30s via `heartbeat`.

The heartbeat only fires if the body **yields to the event loop** — i.e. it's
async and awaits something. All the naturally-long effects (provider calls, MCP
tools) are I/O-bound and async, so this holds. A fully blocking synchronous body
starves the heartbeat; wrap CPU-bound work in a thread
(`asyncio.to_thread(...)`) so the loop stays live. This is the reason for the
"offload blocking work" guidance in [Tools](tools.md).

## Signing

Both directions of the agent↔kernel channel are authenticated with the shared
agent secret:

- **Kernel → agent (webhook):** the kernel signs the request body with
  HMAC-SHA256; the agent verifies `Rebuno-Signature: sha256=...` with
  `hmac.compare_digest` before doing anything. Bad or missing signature → `401`.
- **Agent → kernel:** the agent's `KernelClient` signs every request body the
  same way and sends `Rebuno-Signature` plus `Rebuno-Agent-Id`. Step submissions
  also send the SDK-computed id in `Rebuno-Step-Id` for the kernel to validate.

The separate [`Client`](client.md) uses Bearer auth (`REBUNO_API_KEY`) instead —
it's a client/admin caller, not the agent.
