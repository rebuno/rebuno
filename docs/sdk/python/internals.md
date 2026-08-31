# How it works

This page covers the machinery under `@tool`, `http_client()`, `step()`, and
resume.

## The execution context

On each dispatch the agent builds an `ExecutionContext` and sets it as an
ambient value (a `contextvars.ContextVar`) for the life of the handler. Every
recording primitive finds it there, so you thread no object through your code:

- `@tool` and `wrap_tool` call `ctx.invoke_tool(...)`
- `step()` calls `ctx.invoke_tool(..., kind="local")`
- `http_client()` calls `ctx.begin_llm(...)`, then `ctx.record_llm(...)` once the
  provider response is in hand

Any of these called with no active context raises `RuntimeError`. Read the
context yourself with `rebuno.execution()`, which returns it or raises if there
is none.

## Determinism and step ids

The kernel derives each effect's step id from its content. The SDK sends the
kind, target, and args with the dispatch id from the webhook, and the id comes
back in the decision. The SDK computes nothing and keeps no step state between
calls.

The id is built from five fields:

```
step_id = sha256( length_prefixed(execution_id, kind, target, args_hash, occurrence) )
args_hash = sha256( canonical_json(args) )
```

- `execution_id` scopes the id to one execution.
- `kind` is `tool_call`, `llm_call`, or `local`.
- `target` is the tool id, or the model id for an LLM call.
- `args_hash` is a hash of the arguments, canonicalized by the kernel.
- `occurrence` disambiguates identical calls. The kernel counts how many times
  each `(kind, target, args_hash)` triple has appeared in this dispatch. Calling
  the same tool with the same args twice produces two distinct steps, occurrence
  0 then 1. The counter is cleared when a dispatch is claimed, so every delivery
  attempt counts from zero.

The kernel can only replay what it recorded. Recorded steps come back with their
original values, LLM output included, so the branches don't change. Reading the
clock or picking a random value directly gives different arguments on the second
run. Different arguments mean a different step id, and the effect runs again for
real. Wrap those in [`rebuno.step()`](steps.md) instead.

Step arguments have to be JSON-serializable. Anything else raises `TypeError`
when the SDK encodes the submission, before the body runs.

## Submitting and deciding a step

For each effect the context asks the kernel what to do before running the body.
`submit_step` returns a `StepDecision`:

| Decision | Meaning | SDK behavior |
|----------|---------|--------------|
| `proceed` | new step, run it | runs the body, then records the result |
| `replay` | already recorded | returns the stored result, or raises the stored error; the body never runs |
| `denied` | policy denial | raises `PolicyError` |
| `rate_limited` | policy rate limit | raises `RateLimited` |
| `blocked` / `execution_blocked` | an approval is pending | raises `Blocked`, and the dispatch unwinds |
| `execution_terminal` | the execution is terminal | raises `Terminated` |

On `proceed` the body runs. Success calls `complete_step` with the result.
Failure calls `fail_step`, and for tools raises `ToolError`.

`Blocked` and `Terminated` are also recorded on the context, so the agent
re-raises one even if your handler swallowed it. See
[Dispatch and resume](agents.md#dispatch-and-resume).

## Heartbeats and leases

A dispatch holds a lease so the kernel won't reclaim it and re-deliver while the
handler is still running. Handlers routinely outlive a fixed lease, so the
context wraps the whole handler in a background task that calls `heartbeat`
every 30 seconds.

The lease is the `(dispatch_id, dispatch_attempt)` pair the webhook arrived
with, and every mutation sends it back. A handler whose dispatch was reclaimed
and re-delivered is therefore refused rather than writing alongside the attempt
that replaced it. Its next heartbeat raises `LeaseSuperseded`, which cancels the
handler.

The heartbeat only fires if the handler yields to the event loop. Everything
naturally long in a handler (provider calls, MCP tools, kernel round-trips) is
I/O-bound and async, so this holds. A fully blocking synchronous body starves
it, so wrap CPU-bound work in a thread with `asyncio.to_thread(...)`. That is
the reason for the guidance in [Tools](tools.md#blocking-work).

## Signing

Both directions of the agent-kernel channel are authenticated with the shared
agent secret.

- Kernel to agent (the webhook): the kernel signs the body with HMAC-SHA256, and
  the agent verifies `Rebuno-Signature: sha256=...` with `hmac.compare_digest`
  before doing anything. A bad or missing signature gets a `401`.
- Agent to kernel: `KernelClient` signs every request body the same way and
  sends `Rebuno-Signature` plus `Rebuno-Agent-Id`. Step submissions also send
  `Rebuno-Dispatch-Id`, which scopes the kernel's occurrence counter.

[`Client`](client.md) uses Bearer auth (`REBUNO_API_KEY`) instead, since it is a
client caller rather than the agent.
