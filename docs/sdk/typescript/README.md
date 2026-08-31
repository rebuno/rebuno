# Rebuno TypeScript SDK

Build agents and clients for Rebuno. The SDK is ESM-only, has no runtime
dependencies, and needs Node 22 or later. See
[Getting started](getting-started.md) to install and run it.

## What you write

You write one ordinary async function. Rebuno runs that handler for you. Route
every non-deterministic effect through the kernel, which records the outcome.
Three ways to do it:

| You write | Records | Use it for |
|-----------|---------|------------|
| [`defineTool`](tools.md) | a `tool_call` step | actions the agent takes, such as a search, an email, or a database write |
| [`rebunoFetch`](llm-calls.md) | an `llm_call` step | calls to an LLM provider |
| [`step`](steps.md) | a `local` step | local non-determinism such as the clock, a random choice, or a fresh id |

On a re-dispatch the handler runs again from the top. Any step with a recorded
result replays it instead of running a second time.

## Sections

- **[Getting started](getting-started.md)**: install, configuration, the
  dispatch loop, and a complete example.
- **[Agents](agents.md)**: the `Agent` host, input validation, `serve` vs
  `fetch`, dispatch and resume, lifecycle.
- **[Tools](tools.md)**: `defineTool`, `wrapTool`, idempotency, blocking work,
  and wrapping MCP tools.
- **[LLM calls](llm-calls.md)**: `rebunoFetch` and `createRebunoFetch`.
- **[Steps](steps.md)**: `step()` for durable local work.
- **[Clients](client.md)**: creating and inspecting executions, and approvals.
- **[Errors](errors.md)**: the error class hierarchy.
- **[How it works](internals.md)**: step identity, replay, heartbeats, and the
  kernel protocol.
