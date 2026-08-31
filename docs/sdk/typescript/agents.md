# Agents

`Agent` serves an HTTP webhook and runs your handler once per dispatch. It runs
under an active [execution context](internals.md), so `defineTool`,
`rebunoFetch`, and `step()` record durably.

```ts
import { Agent } from "rebuno";

const agent = new Agent(
  "dev-agent",                    // agentId (required, non-empty)
  {
    secret: "dev-secret",         // or REBUNO_AGENT_SECRET
    baseUrl: "http://localhost:8080",  // or REBUNO_URL
    webhookPath: "/webhook",      // default
    kernelTimeout: 35000,         // ms; default; timeout for agent→kernel calls
    inputSchema: mySchema,        // optional Standard Schema validator
  },
);
```

## The handler

The handler is the async function you hand to `agent.serve()` or `agent.bind()`,
named `process` in the examples below. It takes one argument, the execution's
`input` object, passed through unchanged:

```ts
async function process(input: { prompt: string; limit?: number }) {
  // input === the object passed to client.create("dev-agent", { prompt, limit })
  return { answer: "..." };
}
```

The return value becomes the execution's `output` and has to be
JSON-serializable.

### Validating input

Pass an `inputSchema`, any [Standard Schema](https://standardschema.dev)
validator such as Zod, Valibot, or ArkType, to validate and coerce `input`:

```ts
import { z } from "zod";

const agent = new Agent("dev-agent", {
  secret: "dev-secret",
  baseUrl: "http://localhost:8080",
  inputSchema: z.object({ prompt: z.string(), limit: z.number().default(10) }),
});
```

Validation happens before your handler runs. A failure fails the execution with
the collected issue messages. Your handler receives the validated and coerced
value. Without an `inputSchema`, `input` is passed through as-is.

## `serve()` vs `fetch`

`serve()` binds the handler and serves it with `node:http`. It blocks, resolving
only when the server closes:

```ts
await agent.serve({ host: "0.0.0.0", port: 5000 }, process);
```

You can also bind separately and serve later:

```ts
agent.bind(process);
await agent.serve({ port: 5000 });
```

Use `agent.fetch` to mount the agent into an existing service or an edge
runtime. It is a Web-standard `(Request) => Promise<Response>` handler with the
webhook logic already wired:

```ts
agent.bind(process);

app.post("/webhook", agent.fetch);   // Express-style adapter
export const POST = agent.fetch;     // Next.js / Hono / edge route
```

`agent.fetch` reads the request body, verifies the signature, and returns the
response, so it doesn't care what server calls it. `agent.serve` is a thin
`node:http` wrapper around it.

## Dispatch and resume

Each webhook POST carries an `execution_id`, a `dispatch_id`, and a
`dispatch_attempt`. The agent:

1. Verifies the `Rebuno-Signature` header (see [Signing](internals.md#signing)).
   A bad or missing signature gets a `401`, and a body missing any of the three
   gets a `400`.
2. Acknowledges immediately. The handler runs in a background task and the
   webhook returns `200` right away, so delivery isn't held open for the whole
   execution.
3. Aborts the handler still running for that execution when the webhook
   supersedes it, so a re-delivery doesn't leave two copies racing. The
   superseded run's kernel client is scoped to the aborted signal, so it can
   neither renew the lease nor write for a dispatch it no longer owns. A repeat
   of the attempt already running, or of one the kernel has moved past, is
   ignored.
4. Skips terminal executions. Nothing to do if it is already `completed`,
   `failed`, or `cancelled`.
5. Runs. It fetches the execution's input, validates it, and calls your handler
   under the ambient execution context.

The same handler runs on every dispatch, and each recorded step returns its
stored result instead of executing again. See [How it works](internals.md) for
what breaks this.

A `Blocked` or `Terminated` your handler swallows still ends the dispatch
correctly, because the execution context remembers it and `Agent` re-throws it.
A denial or rate limit has no such backstop and has to escape the handler, but it
may arrive wrapped in a provider SDK's own error type, which `raiseForRefusal()`
unwraps.

## What happens on failure

The agent maps outcomes from your handler onto the execution:

| Outcome | Effect |
|---------|--------|
| returns normally | execution completes with the return value as output |
| throws `Blocked` or `Terminated` | control-flow signals. An approval is pending, or the execution is terminal. The dispatch unwinds and the kernel's state stands |
| throws `PolicyError` or `RateLimited` | execution is failed with the reason. `defineTool` catches `PolicyError` and returns a string, so a denial reaches here only from an LLM call or a `step()`. `RateLimited` reaches here from any of them |
| throws `ToolError` | execution is failed with the message |
| throws any other error | logged, and the execution is failed with the message |

See [Errors](errors.md) for what each class means.

## Lifecycle

```ts
await agent.join();   // await all in-flight execution handlers (best-effort)
await agent.close();  // same as join(); lets in-flight handlers settle
```

`serve()` keeps running until the server closes. Call these directly only when
you drive `agent.fetch` yourself and want to drain in-flight handlers before
shutdown.
