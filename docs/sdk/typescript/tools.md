# Tools

A tool is an action your agent takes. Defining a tool routes every call through
the kernel, so it's recorded as a `tool_call` step — subject to policy, replayed
on resume, and visible in the execution's audit trail.

## `defineTool`

```ts
import { defineTool } from "rebuno";

const search = defineTool({
  name: "search",
  description: "Search the corpus",              // shown to the LLM
  inputSchema: {                                  // JSON Schema, passed to your framework
    type: "object",
    properties: { query: { type: "string" }, limit: { type: "number", default: 10 } },
    required: ["query"],
  },
  execute: async ({ query, limit = 10 }: { query: string; limit?: number }) => {
    // ... do the work
    return [`result for ${query}`];
  },
});
```

`defineTool` returns a plain object — `{ name, description, inputSchema,
idempotency, execute }` — with a durable `execute`. Two ways to use it:

```ts
// call it yourself inside a handler
const hits = await search.execute({ query: "hello" });

// or hand it to your LLM framework (e.g. the Vercel AI SDK) as part of a tool set
```

Because the SDK is framework-agnostic, it doesn't register tools with a model for
you — you wire `search` into whatever agent framework you use, and its `execute`
carries the durability. There's a small adapter pattern for the Vercel AI SDK:
wrap `search.inputSchema` with `jsonSchema(...)` and point the AI SDK tool's
`execute` at `search.execute`. See the
[TypeScript examples](../../../examples/typescript) for a working `asAiTool`
helper.

### Idempotency

`idempotency` controls whether a tool may re-run when a dispatch replays:

- **`safe_to_retry`** (default) — reads and other operations that are fine to
  execute again. On resume, the recorded result replays; if the step never
  completed, it can run again.
- **`at_most_once`** — destructive or non-idempotent operations that must not be
  repeated (sending an email, charging a card). The kernel guarantees the effect
  body runs at most once even across crashes.

```ts
const sendEmail = defineTool({
  name: "send_email",
  idempotency: "at_most_once",
  execute: async ({ to, body }: { to: string; body: string }) => { /* ... */ },
});
```

### Blocking work

The event loop must stay responsive — the kernel lease is renewed by a background
heartbeat that only fires if the effect body yields (see
[How it works](internals.md)). Offload blocking or CPU-bound work (e.g. to a
worker thread) so the loop stays live; an `async` body that awaits I/O is already
fine.

### Calling context

A tool records against the current execution. Calling one outside an active
dispatch (i.e. not inside a handler running under `agent.serve()`/`agent.fetch`,
or a test context) throws an `Error`.

## `wrapTool`

`defineTool` fits functions you write. `wrapTool` builds a Rebuno-routed tool
from a `name` plus an `invoke(args)` seam, for tools that aren't plain functions
— framework tool objects, or schema-only tools:

```ts
import { wrapTool } from "rebuno";

const search = wrapTool({
  name: "search",                                 // tool id the LLM and kernel see
  invoke: (args) => myClient.search(args),        // awaitable or plain return
  description: "Search the corpus",
  inputSchema: {                                  // exposed for framework introspection
    type: "object",
    properties: { query: { type: "string" } },
    required: ["query"],
  },
  idempotency: "safe_to_retry",
  toResult: (raw) => raw,                          // map invoke's return to a JSON-serializable value
  transformArgs: (args) => args,                  // map the caller's arg object before recording/invoking
});
```

- `name` is the tool id both the LLM and the kernel see — put any namespace
  prefix directly in it.
- `toResult` maps the raw return before it's recorded (default: identity).
- `transformArgs` maps the argument object before it's recorded and passed to
  `invoke` (default: a shallow copy) — e.g. null-stripping.

## MCP tools

`wrapMcpTools` wraps [Model Context Protocol](https://modelcontextprotocol.io)
tool descriptors, so tools served over MCP get the same durability and policy as
native ones.

```ts
import { wrapMcpTools } from "rebuno";

const listed = await client.listTools();          // { tools: [...] } from the MCP client
const tools = wrapMcpTools(listed.tools, {
  call: (name, args) => client.callTool({ name, arguments: args }),  // call(name, args) → result
  prefix: "docs",                                  // tool id becomes `${prefix}_${name}`
});
```

- Descriptors can be attribute-style (the official `@modelcontextprotocol/sdk`
  `Tool`) or plain objects with `name` / `description` / `inputSchema` — both
  work.
- `prefix` namespaces the tool id: the LLM and kernel see `${prefix}_${name}`,
  while the MCP server (via `call`) sees the bare `name`. Empty prefix uses the
  name as-is.
- By default the result is flattened from a standard MCP `CallToolResult`
  (`structuredContent` if present, otherwise joined text blocks), and `null`
  arguments are stripped (LLMs often fill optional fields with `null`, which
  typed MCP parameters reject). Override with `toResult` / by wrapping yourself.

`wrapMcpTool` does one descriptor; `wrapMcpTools` maps over a list.
