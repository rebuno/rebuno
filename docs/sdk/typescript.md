# TypeScript SDK

Install the SDK:

```bash
npm install rebuno
```

## Authentication

The kernel uses `--bearer-token` / `REBUNO_BEARER_TOKEN` to configure bearer token authentication. The TypeScript SDK accepts an `apiKey` parameter which maps to this bearer token. The SDK sends the token as an `Authorization: Bearer <apiKey>` header on all HTTP requests and SSE connections to the kernel.

```typescript
const agent = new MyAgent({
  agentId: "researcher",
  kernelUrl: "http://localhost:8080",
  apiKey: "your-secret-token", // maps to --bearer-token on the kernel
});
```

In development mode (when the kernel is started without `--bearer-token`), `apiKey` can be omitted.

## Writing an Agent

### Local Tools

Register tools with `agent.addTool()`. The agent holds the implementation and executes it in-process after the kernel approves the intent.

```typescript
import { z } from "zod";
import { BaseAgent, AgentContext, defineTool } from "rebuno";

class MyAgent extends BaseAgent {
  async process(ctx: AgentContext) {
    const tools = ctx.getTools();
    const result = await tools[0].execute({ query: "what is rebuno" });
    return { answer: result };
  }
}

const agent = new MyAgent({
  agentId: "my-agent",
  kernelUrl: "http://localhost:8080",
});

agent.addTool(
  defineTool({
    id: "web.search",
    description: "Search the web for information.",
    inputSchema: z.object({ query: z.string() }),
    execute: async (input) => {
      return { results: await doSearch(input.query) };
    },
  }),
);

agent.run();
```

### Remote Tools

Declare tool schemas with `agent.addRemoteTool()`. The execute function is never called — a separate runner process handles execution.

```typescript
agent.addRemoteTool(
  defineTool({
    id: "web.search",
    description: "Search the web for information.",
    inputSchema: z.object({ query: z.string() }),
    execute: async () => ({}), // body is never called
  }),
);
```

The rest of the agent code is identical. `ctx.getTools()` returns wrapped tools that route to the runner transparently.

### Mixing Local and Remote Tools

An agent can use both. The SDK routes each call to the right place.

```typescript
agent.addTool(
  defineTool({
    id: "calculator",
    description: "Evaluate a math expression.",
    inputSchema: z.object({ expression: z.string() }),
    execute: async (input) => ({ result: eval(input.expression) }),
  }),
);

agent.addRemoteTool(
  defineTool({
    id: "web.search",
    description: "Search the web for information.",
    inputSchema: z.object({ query: z.string() }),
    execute: async () => ({}),
  }),
);
```

### MCP Tools

Connect to MCP servers to use their tools as local tools. Requires `@modelcontextprotocol/sdk` as a dependency.

```typescript
const agent = new MyAgent({
  agentId: "my-agent",
  kernelUrl: "http://localhost:8080",
});

// Stdio server
agent.mcpServer({
  name: "filesystem",
  command: "npx",
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
});

// HTTP server
agent.mcpServer({ name: "github", url: "http://localhost:3000/mcp" });
```

MCP tools are prefixed with the server name (e.g., `filesystem.read_file`) and appear alongside local and remote tools in `ctx.getTools()`. See [Tools](../tools.md) for namespacing, config-based setup, and partial failure behavior.

You can also load MCP servers from a config object:

```typescript
agent.mcpServersFromConfig({
  filesystem: {
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
  },
  github: {
    url: "http://localhost:3000/mcp",
  },
});
```

### Framework Integration

`ctx.getTools()` returns `WrappedTool` objects that can be converted to framework-specific formats using adapters.

```typescript
// Vercel AI SDK
import { toVercelTools } from "rebuno/tools/adapters/vercel";
const tools = toVercelTools(ctx.getTools());

// LangChain
import { toLangchainTools } from "rebuno/tools/adapters/langchain";
const tools = await toLangchainTools(ctx.getTools());

// Mastra
import { toMastraTools } from "rebuno/tools/adapters/mastra";
const tools = toMastraTools(ctx.getTools());
```

### Direct Tool Invocation

You can also invoke tools directly without `getTools()`:

```typescript
class MyAgent extends BaseAgent {
  async process(ctx: AgentContext) {
    // Invoke and wait for result
    const result = await ctx.invokeTool("web.search", { query: "rebuno" });

    // Submit multiple tools in parallel
    const stepA = await ctx.submitTool("web.search", { query: "topic A" });
    const stepB = await ctx.submitTool("web.search", { query: "topic B" });
    const results = await ctx.awaitSteps([stepA, stepB]);

    // Wait for an external signal (human approval, webhook, etc.)
    const approval = await ctx.waitSignal("approval");

    return { answer: results };
  }
}
```

## Constructor Parameters

### BaseAgent

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `agentId` | string | *required* | Unique identifier for this agent. Must match the agent ID used in policy and execution creation. |
| `kernelUrl` | string | *required* | URL of the rebuno kernel (e.g., `http://localhost:8080`). |
| `apiKey` | string | `""` | Bearer token for authenticating with the kernel. Maps to the kernel's `--bearer-token` / `REBUNO_BEARER_TOKEN`. |
| `consumerId` | string | `""` | Unique identifier for this SSE connection instance. If empty, auto-generated as `{agentId}-{random}`. See [Consumer ID](#consumer-id). |
| `reconnectDelay` | number | `3.0` | Base delay in seconds before reconnecting after an SSE connection failure. |
| `maxReconnectDelay` | number | `60.0` | Maximum delay in seconds between reconnection attempts (exponential backoff cap). |

### BaseRunner

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `runnerId` | string | *required* | Unique identifier for this runner. |
| `kernelUrl` | string | *required* | URL of the rebuno kernel. |
| `capabilities` | string[] | `[]` | List of tool IDs this runner can execute (e.g., `["web.search", "doc.fetch"]`). |
| `apiKey` | string | `""` | Bearer token for authenticating with the kernel. Maps to the kernel's `--bearer-token` / `REBUNO_BEARER_TOKEN`. |
| `name` | string | `""` | Human-readable name for the runner. Defaults to `runnerId` if empty. |
| `reconnectDelay` | number | `2.0` | Base delay in seconds before reconnecting after an SSE connection failure. |
| `maxReconnectDelay` | number | `60.0` | Maximum delay in seconds between reconnection attempts (exponential backoff cap). |

Note: The runner's `consumerId` is auto-generated as `{runnerId}-{random}` and cannot be overridden via the constructor.

### AgentContext

Created internally by the SDK when the kernel assigns an execution. You do not construct it directly. See [AgentContext Reference](#agentcontext-reference) for the full list of methods and properties available inside `process()`.

## Consumer ID

The `consumerId` identifies a specific SSE connection instance for a given agent. It serves several purposes:

- **Multiple consumers**: Multiple processes can connect with the same `agentId` but different `consumerId` values for redundancy and load distribution.
- **Round-robin assignment**: The kernel round-robins execution assignments across all connected consumers for the same agent.
- **Uniqueness**: Each `consumerId` must be unique per connection. If omitted, the SDK auto-generates one.

```typescript
// Two instances of the same agent for redundancy
const agent1 = new MyAgent({
  agentId: "researcher",
  kernelUrl: "...",
  consumerId: "researcher-instance-1",
});
const agent2 = new MyAgent({
  agentId: "researcher",
  kernelUrl: "...",
  consumerId: "researcher-instance-2",
});
```

## AgentContext Reference

| Method | Description |
|---|---|
| `ctx.getTools()` | Return framework-compatible wrapped tools |
| `ctx.invokeTool(toolId, arguments, idempotencyKey?)` | Invoke a tool and wait for the result |
| `ctx.submitTool(toolId, arguments, idempotencyKey?)` | Submit a tool invocation, return `stepId` immediately |
| `ctx.awaitSteps(stepIds)` | Wait for multiple parallel tool invocations to complete |
| `ctx.waitSignal(signalType)` | Wait until an external signal is received |
| `ctx.complete(output?)` | Mark execution as complete |
| `ctx.fail(error)` | Mark execution as failed |

| Property | Description |
|---|---|
| `ctx.executionId` | Current execution ID |
| `ctx.sessionId` | Current session ID |
| `ctx.agentId` | Agent ID |
| `ctx.input` | Input data from the execution request |
| `ctx.labels` | Execution labels |
| `ctx.history` | Previous steps in this execution |

## Writing a Runner

Runners execute tools on behalf of agents that use `addRemoteTool()`. They maintain a persistent SSE connection to the kernel, receive job assignments via push, and report results over HTTP.

```typescript
import { BaseRunner } from "rebuno";

class MyRunner extends BaseRunner {
  async execute(toolId: string, args: unknown): Promise<unknown> {
    const arguments_ = args as Record<string, unknown>;
    if (toolId === "web.search") {
      return { results: await doSearch(arguments_.query as string) };
    }
    throw new Error(`Unknown tool: ${toolId}`);
  }
}

const runner = new MyRunner({
  runnerId: "my-runner",
  kernelUrl: "http://localhost:8080",
  capabilities: ["web.search"],
});

runner.run();
```

### MCP-backed Runner

A runner can serve MCP tools without any custom `execute()` logic:

```typescript
import { BaseRunner } from "rebuno";

const runner = new BaseRunner({
  runnerId: "mcp-tools",
  kernelUrl: "http://localhost:8080",
});

runner.mcpServer({
  name: "filesystem",
  command: "npx",
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
});

runner.run();
```

MCP tool IDs are automatically registered as capabilities. Jobs for MCP tools are routed directly to the MCP server; non-MCP tools fall through to `execute()`.

## Using the Client

The `RebunoClient` provides a low-level API for interacting with the kernel directly.

```typescript
import { RebunoClient } from "rebuno";

const client = new RebunoClient({
  baseUrl: "http://localhost:8080",
  apiKey: "your-secret-token", // optional
});

// Create an execution
const execution = await client.createExecution("my-agent", {
  query: "what is rebuno",
});

// Stream events
for await (const event of client.streamEvents(execution.id)) {
  console.log(event.type, event.payload);
}

// Send a signal (e.g., human approval)
await client.sendSignal(execution.id, "approval", { approved: true });
```

### RebunoClient Options

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `baseUrl` | string | *required* | Base URL of the rebuno kernel. |
| `apiKey` | string | `undefined` | Bearer token for authentication. |
| `timeout` | number | `35000` | Request timeout in milliseconds. |
| `maxRetries` | number | `3` | Maximum retry attempts for failed requests. |
| `retryBaseDelay` | number | `1.0` | Base delay in seconds between retries. |
| `retryMaxDelay` | number | `10.0` | Maximum delay in seconds between retries. |
| `fetch` | FetchFn | `globalThis.fetch` | Custom fetch function. |

### Client Methods

| Method | Description |
|---|---|
| `createExecution(agentId, input?, labels?)` | Start a new execution |
| `getExecution(executionId)` | Get execution details |
| `listExecutions(options?)` | List executions with optional filters |
| `cancelExecution(executionId)` | Cancel a running execution |
| `sendSignal(executionId, signalType, payload?)` | Send a signal to an execution |
| `getEvents(executionId, afterSequence?, limit?)` | Get events for an execution |
| `streamEvents(executionId, afterSequence?)` | Stream events as an async generator |
| `health()` | Check kernel health |
| `ready()` | Check kernel readiness |

## Tool Adapters

The SDK provides adapters for popular AI frameworks. Each adapter converts `WrappedTool` objects from `ctx.getTools()` into the format expected by the framework.

### Vercel AI SDK

```typescript
import { toVercelTools } from "rebuno/tools/adapters/vercel";

const tools = toVercelTools(ctx.getTools());
// Returns Record<string, CoreTool> keyed by sanitized tool name
```

### LangChain

```typescript
import { toLangchainTools } from "rebuno/tools/adapters/langchain";

const tools = await toLangchainTools(ctx.getTools());
// Returns StructuredTool[] for use with LangChain agents
```

### Mastra

```typescript
import { toMastraTools } from "rebuno/tools/adapters/mastra";

const tools = toMastraTools(ctx.getTools());
// Returns tools with { context } execute signature
```

### External Tools

You can also register tools from external frameworks directly:

```typescript
import { tool } from "ai"; // Vercel AI SDK

agent.addExternalTool("my-tool", tool({
  description: "...",
  parameters: z.object({ ... }),
  execute: async (input) => { ... },
}));
```

The `ToolRegistry` auto-detects Vercel, LangChain, and Mastra tool formats.

## Timeouts

Step timeouts are controlled by policy rules. When a policy rule includes `timeout_ms` in its `then` block, that value is used as the step deadline. Otherwise the global `StepTimeout` (default: 5 min) applies. See [Policy](../policy.md) for details.
