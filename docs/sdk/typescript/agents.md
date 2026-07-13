# Agents

`Agent` is the runtime that hosts your handler. It serves an HTTP webhook,
receives dispatches from the kernel, and runs your handler once per dispatch with
an active [execution context](internals.md) so that `defineTool`, `rebunoFetch`,
and `step()` record durably.

```ts
import { Agent } from "rebuno";

const agent = new Agent(
  "dev-agent",                    // agentId (required, non-empty)
  {
    secret: "dev-secret",         // or REBUNO_AGENT_SECRET
    kernelUrl: "http://localhost:8080",  // or REBUNO_URL
    webhookPath: "/webhook",      // default
    kernelTimeout: 35000,         // ms; default; timeout for agent→kernel calls
    inputSchema: mySchema,        // optional Standard Schema validator (see below)
  },
);
```

## The handler

Your handler is any async function. It receives the execution's `input` object
**unchanged** — one argument, the whole input:

```ts
async function process(input: { prompt: string; limit?: number }) {
  // input === the object passed to client.create("dev-agent", { prompt, limit })
  return { answer: "..." };
}
```

The return value becomes the execution's `output` and must be JSON-serializable.

### Validating input

Pass an optional `inputSchema` — any [Standard Schema](https://standardschema.dev)
validator (Zod, Valibot, ArkType, …) — to validate and coerce `input` before your
handler runs:

```ts
import { z } from "zod";

const agent = new Agent("dev-agent", {
  secret: "dev-secret",
  kernelUrl: "http://localhost:8080",
  inputSchema: z.object({ prompt: z.string(), limit: z.number().default(10) }),
});
```

Validation happens *before* your handler runs. A validation failure fails the
execution with the collected issue messages and your handler is never called. The
validated (and coerced) value is what your handler receives. Without an
`inputSchema`, `input` is passed through as-is.

## `serve()` vs `fetch`

The simple path binds the handler and serves it with `node:http`. This **blocks**
(resolves only when the server closes):

```ts
await agent.serve({ host: "0.0.0.0", port: 5000 }, process);
```

You can also bind separately and serve later:

```ts
agent.bind(process);
await agent.serve({ port: 5000 });
```

To mount the agent into an existing service or an edge runtime, use `agent.fetch`
— a Web-standard `(Request) => Promise<Response>` handler with the webhook logic
already wired:

```ts
agent.bind(process);

app.post("/webhook", agent.fetch);   // Express-style adapter
export const POST = agent.fetch;     // Next.js / Hono / edge route
```

`agent.fetch` reads the request body, verifies the signature, and returns the
response — it doesn't care what server calls it. `agent.serve` is just a thin
`node:http` wrapper around it.

## Dispatch and resume

Each webhook POST carries an `execution_id`. The agent:

1. **Verifies the signature.** The body is HMAC-SHA256'd with the agent secret
   and compared against the `Rebuno-Signature: sha256=...` header. A bad or
   missing signature returns `401`; a body with no `execution_id` returns `400`.
2. **Acknowledges immediately.** The handler runs in a background task and the
   webhook returns `200` right away, so the kernel's delivery isn't held open for
   the whole execution.
3. **Skips terminal executions.** If the execution is already
   `completed`/`failed`/`cancelled`, there's nothing to do.
4. **Hydrates and runs.** It loads the execution's already-terminal steps in one
   read (so replay is local, not a round trip per step), sets the ambient
   execution context, validates/binds the input, and runs your handler.

Because the same handler runs on every dispatch, **resume is just re-running with
replay**: each recorded step returns its stored result instead of executing
again, so the handler fast-forwards to where it left off. You don't write resume
logic. See [How it works](internals.md) for the identity and replay mechanics
that make this safe — and why non-determinism outside a recorded step will break
it.

## What happens on failure

The agent maps outcomes from your handler onto the execution:

| Outcome | Effect |
|---------|--------|
| returns normally | execution **completes** with the return value as output |
| throws `Blocked` / `Terminated` | internal control-flow signals (an approval is pending, or the execution is terminal) — the dispatch unwinds cleanly and returns `200`; not an error |
| throws `PolicyError`, `ToolError`, `RateLimited`, `StepIDMismatch` | execution is **failed** with the message |
| throws any other error | logged, execution is **failed** with the message |

See [Errors](errors.md) for what each class means.

## Lifecycle

```ts
await agent.join();   // await all in-flight execution handlers (best-effort)
await agent.close();  // same as join() — lets in-flight handlers settle
```

`serve()` keeps running until the server closes. Call `join()` / `close()`
directly only when you drive `agent.fetch` yourself and want to drain in-flight
handlers before shutdown.
