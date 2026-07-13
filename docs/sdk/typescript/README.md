# Rebuno TypeScript SDK

The TypeScript SDK lets you build agents that run on Rebuno's kernel. Rebuno
gives those agents three things:

- **Durable execution** — an agent can crash, be redeployed, or block on a human
  approval, and resume without re-running side effects it already performed.
- **An event-sourced record** — every tool call, LLM call, and unit of local
  work an agent does is recorded as a *step* and can be inspected after the fact.
- **Governance** — policy can allow, deny, rate-limit, or require approval for
  any step before it runs.

The SDK is **framework-agnostic** (use it with the Vercel AI SDK, LangChain,
Mastra, or a bare model client), **ESM-only**, has **zero runtime dependencies**,
and targets **Node 18+** — it relies on the platform's `fetch`, Web Crypto, and
`AsyncLocalStorage`. See [Getting started](getting-started.md) to install and run
it.

## The mental model

An agent is an ordinary async function — your *handler* — that Rebuno runs for
you. The kernel owns the durable state; your process is stateless and
disposable. Two ideas make this work:

1. **Every non-deterministic effect is recorded as a step.** Calling a tool,
   calling an LLM, or reading the clock all go through the kernel, which records
   the result. There are three ways to record an effect, and they are the SDK's
   core surface:

   | You write | Records | Use it for |
   |-----------|---------|------------|
   | [`defineTool`](tools.md) | a `tool_call` step | actions the agent takes (search, send email, query a db) |
   | [`rebunoFetch`](llm-calls.md) | an `llm_call` step | calls to an LLM provider |
   | [`step`](steps.md) | a `tool_call` step | local non-determinism (time, randomness, fresh ids) |

2. **Resume is replay.** When a handler is re-dispatched (after a crash, or when
   an approval is granted), it runs again from the top — but each recorded step
   returns its *stored* result instead of executing again. Your code doesn't
   branch on "is this a resume"; it just runs, and the already-done work is
   short-circuited underneath it. This is why the effects must be deterministic
   in order and wrapped: see [How it works](internals.md).

Around those primitives, [`Agent`](agents.md) is the runtime that hosts your
handler and receives dispatches from the kernel, and [`Client`](client.md)
is what you (or your backend) use to create executions and inspect them.

## Sections

- **[Getting started](getting-started.md)** — install, configuration, the
  request/dispatch loop, and a complete example.
- **[Agents](agents.md)** — the `Agent` runtime: input binding, `serve` vs
  `fetch`, dispatch and resume, lifecycle.
- **[Tools](tools.md)** — `defineTool`, `wrapTool`, idempotency, blocking work,
  and wrapping MCP tools.
- **[LLM calls](llm-calls.md)** — `rebunoFetch` / `createRebunoFetch`.
- **[Steps](steps.md)** — `step()` for durable local work.
- **[Clients](client.md)** — creating and inspecting executions; approvals.
- **[Errors](errors.md)** — the error class hierarchy.
- **[How it works](internals.md)** — step identity, replay, heartbeats, and the
  kernel protocol.
