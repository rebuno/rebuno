# Python SDK

Install the SDK:

```bash
pip install rebuno
```

## Authentication

The kernel uses `--bearer-token` / `REBUNO_BEARER_TOKEN` to configure bearer token authentication. The Python SDK accepts an `api_key` parameter which maps to this bearer token. The SDK sends the token as an `Authorization: Bearer <api_key>` header on all HTTP requests and SSE connections to the kernel.

```python
agent = MyAgent(
    agent_id="researcher",
    kernel_url="http://localhost:8080",
    api_key="your-secret-token",  # maps to --bearer-token on the kernel
)
```

In development mode (when the kernel is started without `--bearer-token`), `api_key` can be omitted.

## Writing an Agent

### Local Tools

Register tools with `@agent.tool()`. The agent holds the implementation and executes it in-process after the kernel approves the intent.

```python
import asyncio
from rebuno import AsyncBaseAgent, AsyncAgentContext

class MyAgent(AsyncBaseAgent):
    async def process(self, ctx: AsyncAgentContext) -> dict:
        tools = ctx.get_tools()
        result = await tools[0]("what is rebuno")
        return {"answer": result}

agent = MyAgent(agent_id="my-agent", kernel_url="http://localhost:8080")

@agent.tool("web.search")
async def web_search(query: str) -> dict:
    """Search the web for information."""
    return {"results": await do_search(query)}

asyncio.run(agent.run())
```

### Remote Tools

Declare tool schemas with `@agent.remote_tool()`. The function body is never called -- a separate runner process handles execution.

```python
@agent.remote_tool("web.search")
async def web_search(query: str) -> dict:
    """Search the web for information."""
    ...  # body is never called
```

The rest of the agent code is identical. `ctx.get_tools()` returns callables that route to the runner transparently.

### Mixing Local and Remote Tools

An agent can use both. The SDK routes each call to the right place.

```python
@agent.tool("calculator")
def calculator(expression: str) -> dict:
    """Evaluate a math expression."""
    return {"result": eval(expression)}

@agent.remote_tool("web.search")
async def web_search(query: str) -> dict:
    """Search the web for information."""
    ...
```

### MCP Tools

Connect to MCP servers to use their tools as local tools. Requires the `mcp` extra: `pip install rebuno[mcp]`.

```python
agent = MyAgent(agent_id="my-agent", kernel_url="http://localhost:8080")

# Stdio server
agent.mcp_server(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)

# HTTP server
agent.mcp_server("github", url="http://localhost:3000/mcp")
```

MCP tools are prefixed with the server name (e.g., `filesystem.read_file`) and appear alongside local and remote tools in `ctx.get_tools()`. See [Tools](tools.md) for namespacing, config-based setup, and partial failure behavior.

### Framework Integration

`ctx.get_tools()` returns callables that preserve the original function's `__name__`, `__doc__`, and type annotations. Any framework that introspects function signatures works out of the box.

```python
# LangGraph
from langgraph.prebuilt import create_react_agent
agent = create_react_agent(model=llm, tools=ctx.get_tools(), prompt=SYSTEM_PROMPT)
```

### Direct Tool Invocation

You can also invoke tools directly without `get_tools()`:

```python
class MyAgent(AsyncBaseAgent):
    async def process(self, ctx: AsyncAgentContext) -> dict:
        # Invoke and wait for result
        result = await ctx.invoke_tool("web.search", {"query": "rebuno"})

        # Submit multiple tools in parallel
        step_a = await ctx.submit_tool("web.search", {"query": "topic A"})
        step_b = await ctx.submit_tool("web.search", {"query": "topic B"})
        results = await ctx.await_steps([step_a, step_b])

        # Wait for an external signal (human approval, webhook, etc.)
        approval = await ctx.wait_signal("approval")

        return {"answer": results}
```

### Sync API

A synchronous API is also available:

```python
from rebuno import BaseAgent, AgentContext

class MyAgent(BaseAgent):
    def process(self, ctx: AgentContext) -> dict:
        tools = ctx.get_tools()
        result = tools[0]("what is rebuno")
        return {"answer": result}

agent = MyAgent(agent_id="my-agent", kernel_url="http://localhost:8080")

@agent.tool("web.search")
def web_search(query: str) -> dict:
    """Search the web for information."""
    return {"results": do_search(query)}

agent.run()
```

## Constructor Parameters

### BaseAgent / AsyncBaseAgent

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `agent_id` | str | *required* | Unique identifier for this agent. Must match the agent ID used in policy and execution creation. |
| `kernel_url` | str | *required* | URL of the rebuno kernel (e.g., `http://localhost:8080`). |
| `api_key` | str | `""` | Bearer token for authenticating with the kernel. Maps to the kernel's `--bearer-token` / `REBUNO_BEARER_TOKEN`. |
| `consumer_id` | str | `""` | Unique identifier for this SSE connection instance. If empty, auto-generated as `{agent_id}-{random}`. See [Consumer ID](#consumer-id). |
| `reconnect_delay` | float | `3.0` | Base delay in seconds before reconnecting after an SSE connection failure. |
| `max_reconnect_delay` | float | `60.0` | Maximum delay in seconds between reconnection attempts (exponential backoff cap). |

### BaseRunner / AsyncBaseRunner

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `runner_id` | str | *required* | Unique identifier for this runner. |
| `kernel_url` | str | *required* | URL of the rebuno kernel. |
| `capabilities` | list[str] | `[]` | List of tool IDs this runner can execute (e.g., `["web.search", "doc.fetch"]`). |
| `api_key` | str | `""` | Bearer token for authenticating with the kernel. Maps to the kernel's `--bearer-token` / `REBUNO_BEARER_TOKEN`. |
| `name` | str | `""` | Human-readable name for the runner. Defaults to `runner_id` if empty. |
| `reconnect_delay` | float | `2.0` | Base delay in seconds before reconnecting after an SSE connection failure. |
| `max_reconnect_delay` | float | `60.0` | Maximum delay in seconds between reconnection attempts (exponential backoff cap). |

Note: The runner's `consumer_id` is auto-generated as `{runner_id}-{random}` and cannot be overridden via the constructor.

### AgentContext / AsyncAgentContext

These are created internally by the SDK when the kernel assigns an execution. You do not construct them directly. See [AgentContext Reference](#agentcontext-reference) for the full list of methods and properties available inside `process()`.

## Consumer ID

The `consumer_id` identifies a specific SSE connection instance for a given agent. It serves several purposes:

- **Multiple consumers**: Multiple processes can connect with the same `agent_id` but different `consumer_id` values for redundancy and load distribution.
- **Round-robin assignment**: The kernel round-robins execution assignments across all connected consumers for the same agent.
- **Uniqueness**: Each `consumer_id` must be unique per connection. If omitted, the SDK auto-generates one.

```python
# Two instances of the same agent for redundancy
agent1 = MyAgent(agent_id="researcher", kernel_url="...", consumer_id="researcher-instance-1")
agent2 = MyAgent(agent_id="researcher", kernel_url="...", consumer_id="researcher-instance-2")
```

## AgentContext Reference

Both `AsyncAgentContext` and `AgentContext` provide the same interface.

| Method | Description |
|---|---|
| `ctx.get_tools()` | Return framework-compatible tool callables |
| `ctx.invoke_tool(tool_id, arguments)` | Invoke a tool and wait for the result |
| `ctx.submit_tool(tool_id, arguments)` | Submit a tool invocation, return `step_id` immediately |
| `ctx.await_steps(step_ids)` | Wait for multiple parallel tool invocations to complete |
| `ctx.wait_signal(signal_type)` | Wait until an external signal is received |

| Property | Description |
|---|---|
| `ctx.execution_id` | Current execution ID |
| `ctx.session_id` | Current session ID |
| `ctx.agent_id` | Agent ID |
| `ctx.input` | Input data from the execution request |
| `ctx.labels` | Execution labels |
| `ctx.history` | Previous steps in this execution |

## Writing a Runner

Runners execute tools on behalf of agents that use `@agent.remote_tool()`. They maintain a persistent SSE connection to the kernel, receive job assignments via push, and report results over HTTP.

```python
import asyncio
from rebuno import AsyncBaseRunner

class MyRunner(AsyncBaseRunner):
    async def execute(self, tool_id: str, arguments: dict) -> dict:
        if tool_id == "web.search":
            return {"results": await do_search(arguments["query"])}
        raise ValueError(f"Unknown tool: {tool_id}")

runner = MyRunner(
    runner_id="my-runner",
    kernel_url="http://localhost:8080",
    capabilities=["web.search"],
)
asyncio.run(runner.run())
```

Sync version:

```python
from rebuno import BaseRunner

class MyRunner(BaseRunner):
    def execute(self, tool_id: str, arguments: dict) -> dict:
        if tool_id == "web.search":
            return {"results": do_search(arguments["query"])}
        raise ValueError(f"Unknown tool: {tool_id}")

runner = MyRunner(
    runner_id="my-runner",
    kernel_url="http://localhost:8080",
    capabilities=["web.search"],
)
runner.run()
```

### MCP-backed Runner

A runner can serve MCP tools without any custom `execute()` logic:

```python
runner = AsyncBaseRunner(
    runner_id="mcp-tools",
    kernel_url="http://localhost:8080",
)
runner.mcp_server(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)
await runner.run()
```

MCP tool IDs are automatically registered as capabilities. Jobs for MCP tools are routed directly to the MCP server; non-MCP tools fall through to `execute()`.

## Timeouts

Step timeouts are controlled by policy rules. When a policy rule includes `timeout_ms` in its `then` block, that value is used as the step deadline. Otherwise the global `StepTimeout` (default: 5 min) applies. See [Policy](policy.md) for details.
