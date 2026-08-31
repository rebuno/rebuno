# Tools

A tool is an action your agent takes. Wrapping a function as a tool routes every
call through the kernel, where it's recorded as a `tool_call` step. Policy
applies to it, it replays on resume, and it shows up in the audit trail.

## `defineTool`

```ts
import { defineTool } from "rebuno";

const search = defineTool({
  name: "search",
  description: "Search the corpus",     // shown to the LLM
  inputSchema: mySchema,                // optional; whatever your framework reads
  idempotency: "safe_to_retry",         // default
  execute: async ({ query, limit = 10 }: { query: string; limit?: number }) => {
    // ...
  },
});
```

The returned value is callable and carries `name`, `description`, `inputSchema`,
`idempotency`, and `execute`, so frameworks that introspect a tool object bind it
unchanged. Hand tools to a framework as a plain array:

```ts
const agent = createAgent(llm, [search, ...]);
```

### Idempotency

`idempotency` controls whether a tool may run again when a dispatch replays.

- `safe_to_retry` (default) is for reads and anything else fine to execute
  again. On resume the recorded result replays, and a step that never completed
  runs again.
- `at_most_once` is for destructive operations such as sending an email or
  charging a card. If a resumed dispatch finds the step already executing, the
  kernel fails it with reason `indeterminate` and the SDK throws `ToolError`, so
  your handler decides how to reconcile. A step recorded but never started is
  still safe, so it runs.

```ts
const sendEmail = defineTool({
  name: "send_email",
  idempotency: "at_most_once",
  execute: async ({ to, body }: { to: string; body: string }) => { /* ... */ },
});
```

### Denied calls

A tool denied by policy does not throw. `defineTool` and `wrapTool` catch the
`PolicyError` and return a string instead:

```
search not allowed. reason: <the rule's reason>
```

The LLM sees that as the tool's result and can pick a different path, rather
than the denial crashing the run. A denied `step()` throws `PolicyError`
normally, since nothing reads its result as a tool response.

### Blocking work

The event loop has to stay responsive. The kernel lease is renewed by a
background timer that only fires while the handler yields (see
[How it works](internals.md#heartbeats-and-leases)). Offload CPU-bound work to a
worker thread so the loop stays live.

### Calling context

A tool records against the current execution. Calling one outside an active
dispatch throws an `Error`. Active means inside a handler running under
`agent.serve()` or `agent.fetch`, or inside a test context.

## `wrapTool`

`defineTool` fits functions you own. `wrapTool` builds a Rebuno-routed tool from
a `name` plus an `invoke(args)` seam. Use it for tools that aren't plain
callables, like framework tool objects or schema-only tools:

```ts
import { wrapTool } from "rebuno";

const search = wrapTool({
  name: "search",
  invoke: (args) => myClient.search(args),   // awaitable or plain return
  description: "Search the corpus",
  inputSchema: mySchema,
  idempotency: "safe_to_retry",
  toResult: undefined,      // map invoke's return to a JSON-serializable value
  transformArgs: undefined, // map the caller's arg object before recording/invoking
});
```

- `name` is the tool id the LLM sees and the kernel sees, so put any namespace
  prefix directly in it.
- `inputSchema` is passed through to the returned tool for frameworks that read
  it.
- `toResult` maps the raw return before it's recorded. Defaults to identity.
- `transformArgs` maps the argument object before it's recorded and passed to
  `invoke`, for something like null-stripping. Defaults to a shallow copy.

## MCP tools

`wrapMcpTool` and `wrapMcpTools` wrap
[Model Context Protocol](https://modelcontextprotocol.io) tool descriptors, so
MCP tools get the same treatment as native ones.

```ts
import { wrapMcpTools } from "rebuno";

const tools = wrapMcpTools(await session.listTools(), {
  call: session.callTool,   // call(toolName, args) → result
  prefix: "docs",           // tool id becomes `${prefix}_${name}`
});
const agent = createAgent(llm, tools);
```

- Descriptors carry `name`, `description`, and `inputSchema`, the spec field
  names.
- `prefix` namespaces the tool id. The LLM and the kernel see
  `` `${prefix}_${name}` ``, while the MCP server (via `call`) sees the bare
  `name`. An empty prefix uses the name as-is.
- The result is flattened from a standard MCP `CallToolResult` by default,
  preferring structured content and otherwise joining text blocks. Override it
  with `toResult`.
- Null arguments are stripped by default, since LLMs often fill optional fields
  with `null` and typed MCP parameters reject it.

`wrapMcpTool` does one descriptor; `wrapMcpTools` maps over a list.
