# How it works

This page covers the machinery under `defineTool`, `rebunoFetch`, `step()`, and
resume.

## The execution context

On each dispatch the agent builds an `ExecutionContext` and sets it as an
ambient value (an `AsyncLocalStorage` from `node:async_hooks`) for the life of
the handler. Every recording primitive finds it there, so you thread no object
through your code:

- `defineTool` and `wrapTool` call `ctx.invokeTool(...)`
- `step()` calls `ctx.invokeTool(..., { kind: "local" })`
- `rebunoFetch` calls `ctx.beginLlm(...)`, then `ctx.recordLlm(...)` once the
  provider response is in hand

Any of these called with no active context throws an `Error`. Read the context
yourself with the exported `execution()` accessor, which returns it or throws if
there is none.

## Determinism and step ids

The kernel derives each effect's step id from its content. The SDK sends the
kind, target, and args with the dispatch id from the webhook, and the id comes
back in the decision. The SDK computes nothing and keeps no step state between
calls.

The id is built from five fields:

```
stepId   = sha256( Σ `${byteLength(field)}:` + bytes(field) )   over the fields below
argsHash = sha256( canonicalJson(args) )
fields   = [ executionId, kind, target, argsHash, occurrence ]
```

- `executionId` scopes the id to one execution.
- `kind` is `tool_call`, `llm_call`, or `local`.
- `target` is the tool id, or the model id for an LLM call.
- `argsHash` is a hash of the arguments, canonicalized by the kernel.
- `occurrence` disambiguates identical calls. The kernel counts how many times
  each `(kind, target, argsHash)` triple has appeared in this dispatch. Calling
  the same tool with the same args twice produces two distinct steps, occurrence
  0 then 1. The counter is cleared when a dispatch is claimed, so every delivery
  attempt counts from zero.

The kernel can only replay what it recorded. Recorded steps come back with their
original values, LLM output included, so the branches don't change. Reading the
clock or picking a random value directly gives different arguments on the second
run. Different arguments mean a different step id, and the effect runs again for
real. Wrap those in [`step()`](steps.md) instead.

Step arguments have to be JSON-serializable. Anything else throws a `TypeError`
when the SDK encodes the submission, before the body runs.

## Submitting and deciding a step

For each effect the context asks the kernel what to do before running the body.
`submitStep` returns a `StepDecision`:

| Decision | Meaning | SDK behavior |
|----------|---------|--------------|
| `proceed` | new step, run it | runs the body, then records the result |
| `replay` | already recorded | returns the stored result, or throws the stored error; the body never runs |
| `denied` | policy denial | throws `PolicyError` |
| `rate_limited` | policy rate limit | throws `RateLimited` |
| `blocked` / `execution_blocked` | an approval is pending | throws `Blocked`, and the dispatch unwinds |
| `execution_terminal` | the execution is terminal | throws `Terminated` |

On `proceed` the body runs. Success calls `completeStep` with the result.
Failure calls `failStep`, and for tools throws `ToolError`.

`Blocked` and `Terminated` are also recorded on the context, so the agent
re-throws one even if your handler swallowed it. See
[Dispatch and resume](agents.md#dispatch-and-resume).

## Heartbeats and leases

A dispatch holds a lease so the kernel won't reclaim it and re-deliver while the
handler is still running. Handlers routinely outlive a fixed lease, so the
context wraps the whole handler in a `setInterval` that calls `heartbeat` every
30 seconds.

The heartbeat only fires if the handler yields to the event loop. Everything
naturally long in a handler (provider calls, MCP tools, kernel round-trips) is
I/O-bound and async, so this holds. A fully synchronous, blocking body starves
it, so offload CPU-bound work to a worker thread. That is the reason for the
guidance in [Tools](tools.md#blocking-work).

A superseded run stops renewing, since the lease belongs to the newer dispatch.

## Signing

Both directions of the agent-kernel channel are authenticated with the shared
agent secret, using HMAC-SHA256 via Web Crypto.

- Kernel to agent (the webhook): the kernel signs the body, and the agent
  verifies `Rebuno-Signature: sha256=...` with a constant-time compare before
  doing anything. A bad or missing signature gets a `401`.
- Agent to kernel: the kernel client signs every request body the same way and
  sends `Rebuno-Signature` plus `Rebuno-Agent-Id`. Step submissions also send
  `Rebuno-Dispatch-Id`, which scopes the kernel's occurrence counter.

[`Client`](client.md) uses Bearer auth (`REBUNO_API_KEY`) instead, since it is a
client caller rather than the agent.
