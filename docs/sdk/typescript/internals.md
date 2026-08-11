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
- `step()` calls `ctx.invokeTool(..., { kind: "local" })`

If any of these runs with no active context (outside a handler), it throws an
`Error`. You can also read the current context ambiently via the exported
`execution()` accessor, which returns the active `ExecutionContext` (and throws if
there isn't one).

## Determinism and step ids

Each effect is identified by a **step id** the kernel derives from the effect's
content. The SDK sends `{kind, target, args}` plus the `dispatch_id` from the
webhook, and gets the id back in the decision. It computes nothing and keeps no
step state between calls.

The id is built from five fields:

```
stepId    = sha256( Σ `${byteLength(field)}:` + bytes(field) )   over the fields below
argsHash  = sha256( canonicalJson(args) )
fields    = [ executionId, kind, target, argsHash, occurrence ]
```

- **`kind`** — `tool_call` or `llm_call`.
- **`target`** — the tool id, or the model id for an LLM call.
- **`argsHash`** — a hash of the arguments, canonicalized by the kernel.
- **`occurrence`** — a counter that disambiguates identical calls. The kernel
  counts how many times each `(kind, target, argsHash)` triple has appeared in
  this dispatch, so calling the same tool with the same args twice produces two
  distinct steps (occurrence 0, then 1). The counter resets when a dispatch is
  claimed, so every delivery attempt counts from zero.

**This is why handlers must be deterministic.** Replay works only if the second
run reaches the same effects as the first: same content, same ids, so the kernel
short-circuits them. If your handler reads the clock, picks a random value, or
generates an id *directly*, a resumed run submits different arguments — a
different `argsHash`, a different step id — and the kernel treats it as a new
effect and runs it for real. Wrap that non-determinism in [`step()`](steps.md) so
the value is recorded once and replayed.

Step arguments must be JSON-serializable (objects, arrays, strings, finite
numbers, booleans, null) — other values (non-finite numbers, functions, symbols)
throw a `TypeError`.

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
  send `Rebuno-Dispatch-Id`, which scopes the kernel's occurrence counter.

The separate [`Client`](client.md) uses Bearer auth (`REBUNO_API_KEY`) instead —
it's a client/admin caller, not the agent.
