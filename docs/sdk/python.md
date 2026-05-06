# Python SDK

Install the SDK:

```bash
pip install rebuno
```

The public surface is small: `Agent`, `Client`, `Runner`, `tool`, `MCPServer`, `remote`, `execution`.

## Authentication

The kernel uses `--bearer-token` / `REBUNO_BEARER_TOKEN` to configure bearer token authentication. The Python SDK accepts an `api_key` parameter which maps to this bearer token, sent as an `Authorization: Bearer <api_key>` header on all HTTP requests and SSE connections.

```python
agent = Agent("researcher", kernel_url="http://localhost:8080", api_key="your-secret-token")
```

When constructed without arguments, `Agent`, `Client`, and `Runner` read `REBUNO_URL` and `REBUNO_API_KEY` from the environment. In development mode (kernel started without `--bearer-token`), `api_key` can be omitted.

## Writing an Agent

An agent is a function plus a one-line entry point. The handler signature is the input schema — required parameters become required input fields; defaults make them optional.

```python
from rebuno import Agent, execution

agent = Agent("my-agent")

async def process(prompt: str) -> dict:
    return {"answer": prompt.upper(), "execution_id": execution.id}

agent.run(process)
```

`execution` is a module-level proxy resolving to the current `ExecutionState` via a `ContextVar`. Use it inside `process` (or any tool) to access `id`, `session_id`, `input`, `labels`, `history`. Outside an active execution, attribute access raises `RuntimeError`.

### Input shapes

The framework introspects the handler signature once and routes `claim.input` to it three ways:

```python
# Kwargs (most common)
async def process(prompt: str, repo_url: str = ""):
    ...

# Pydantic model — validation, defaults, JSON Schema
class Input(BaseModel):
    prompt: str
    repo_url: str = ""

async def process(input: Input):
    ...

# Raw dict — opaque passthrough
async def process(input: dict):
    ...
```

A missing required input fails the execution with a clear error message before the handler runs.

### Local Tools

Tools are plain module-level functions. `@tool` registers them globally; the wrapper submits a kernel intent before running the body.

```python
from rebuno import tool

@tool("web_search")
async def search(query: str, limit: int = 10) -> dict:
    """Search the web."""
    return {"results": await do_search(query, limit)}
```

Pass them to your framework as a list:

```python
async def process(prompt: str):
    graph = create_agent(llm, [search])
    return await graph.ainvoke({"messages": [HumanMessage(prompt)]})
```

### Remote Tools

`@tool(remote=True)` declares a tool whose body lives on a runner. The body is a stub — the kernel routes execution. Useful for type-checked imperative calls in agent code:

```python
@tool("compute.heavy", remote=True)
async def heavy(data: str) -> str:
    """Run on a runner."""

# Inside a handler:
result = await heavy(data="x")
```

For LLM-driven discovery (no source-coupling), use `remote.Tools(prefix)` instead — see [Remote tool discovery](#remote-tool-discovery).

### Mixing local, remote, and MCP

Local `@tool` functions, remote `@tool(remote=True)` stubs, MCP tools, and discovered remote tools can all coexist in one tool list:

```python
graph = create_agent(llm, [
    search,                  # local @tool
    heavy,                   # remote @tool stub
    *github.tools,           # local MCP (MCPServer)
    *compute.tools,          # remote discovery (remote.Tools)
])
```

### Direct invocation via `execution`

You can also invoke tools or wait on signals through the execution proxy:

```python
async def process(prompt: str):
    result = await execution.invoke_tool("web_search", {"query": prompt})
    approval = await execution.wait_signal("approval")
    return {"answer": result, "approved": approval.get("approved")}
```

Most agents won't need this — return a value to complete, raise to fail. The proxy is for the rare case where the framework's tool plumbing isn't enough.

### Framework integration

The wrappers preserve `__name__`, `__doc__`, and synthesized `inspect.Signature`. Any framework that introspects callables works unmodified:

```python
# LangGraph
from langgraph.prebuilt import create_react_agent
graph = create_react_agent(model=llm, tools=[search, *github.tools], prompt=SYSTEM_PROMPT)
```

## Local MCP

Connect to MCP servers directly from the agent process. Requires `pip install rebuno[mcp]`.

```python
from rebuno import MCPServer

# Stdio server
fs = MCPServer(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)

# HTTP server
github = MCPServer(
    "github",
    url="https://api.githubcopilot.com/mcp/",
    headers={"Authorization": "Bearer xxx"},
)
```

Tools are namespaced by `prefix` (defaults to `name`): `filesystem.read_file`, `github.create_pr`, etc.

Splat into your tool list:

```python
graph = create_agent(llm, [..., *fs.tools, *github.tools])
```

Servers connect at `agent.run()` startup and disconnect on shutdown. Tool calls go through the kernel for policy/audit; the MCP transport happens in the agent process.

## Remote tool discovery

Use `remote.Tools(prefix)` to consume tools that *runners* host elsewhere — the agent never holds credentials, never opens MCP transport. Schemas come from the kernel directory ([`GET /v0/tools`](../api.md#tools)).

```python
from rebuno import remote

github  = remote.Tools("github")
compute = remote.Tools("compute")

async def process(prompt: str):
    graph = create_agent(llm, [..., *github.tools, *compute.tools])
    return await graph.ainvoke({"messages": [HumanMessage(prompt)]})
```

Each call submits an intent with `remote=True`; the kernel routes to whichever runner advertises the tool ID. Works for any source the runner publishes — `@tool` Python functions, MCP servers, or both.

Schemas are fetched once at agent startup. Restart the agent to pick up runner-side schema changes.

## Constructor Parameters

### Agent

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `agent_id` | str | *required* | Unique identifier for this agent. Must match the agent ID used in policy and execution creation. |
| `kernel_url` | str \| None | env `REBUNO_URL` | URL of the kernel (e.g., `http://localhost:8080`). |
| `api_key` | str \| None | env `REBUNO_API_KEY` | Bearer token. Maps to the kernel's `--bearer-token`. |
| `consumer_id` | str | `""` | SSE connection identifier. Auto-generated as `{agent_id}-{random}` if empty. See [Consumer ID](#consumer-id). |
| `reconnect_delay` | float | `3.0` | Initial reconnect delay (seconds). |
| `max_reconnect_delay` | float | `60.0` | Cap on reconnect backoff. |

### Runner

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `runner_id` | str | *required* | Unique identifier. |
| `kernel_url` | str \| None | env `REBUNO_URL` | URL of the kernel. |
| `api_key` | str \| None | env `REBUNO_API_KEY` | Bearer token. |
| `capabilities` | Iterable[str] \| None | all `@tool` IDs | Tool IDs to advertise. If omitted, every `@tool` in the registry is advertised plus any tools from MCP servers added with `runner.host(...)`. |
| `consumer_id` | str | `""` | Auto-generated if empty. |

### Client

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `base_url` | str \| None | env `REBUNO_URL` | URL of the kernel. |
| `api_key` | str \| None | env `REBUNO_API_KEY` | Bearer token. |
| `timeout` | float | `35.0` | HTTP timeout (seconds). |
| `max_retries` | int | `3` | Retry attempts on retryable failures. |

## Consumer ID

The `consumer_id` identifies a specific SSE connection instance for a given `agent_id` (or `runner_id`). It serves several purposes:

- **Multiple consumers**: Multiple processes can connect with the same `agent_id` but different `consumer_id` values for redundancy and load distribution.
- **Round-robin assignment**: The kernel round-robins execution assignments across all connected consumers for the same agent.
- **Uniqueness**: Each `consumer_id` must be unique per connection. If omitted, the SDK auto-generates one.

```python
agent1 = Agent("researcher", consumer_id="researcher-instance-1")
agent2 = Agent("researcher", consumer_id="researcher-instance-2")
```

## execution Reference

`from rebuno import execution` exposes the current execution. Valid only inside a handler or a `@tool` body running under `agent.run()`.

| Property | Description |
|---|---|
| `execution.id` | Current execution ID |
| `execution.session_id` | Current session ID |
| `execution.agent_id` | Agent ID |
| `execution.input` | Raw input data from the execution request |
| `execution.labels` | Execution labels |
| `execution.history` | Previous step history |

| Method | Description |
|---|---|
| `await execution.invoke_tool(tool_id, arguments)` | Invoke a tool and wait for the result |
| `await execution.wait_signal(signal_type)` | Wait until an external signal arrives |
| `await execution.complete(output)` | Mark complete (usually unnecessary — return from the handler) |
| `await execution.fail(error)` | Mark failed (usually unnecessary — raise from the handler) |

## Writing a Runner

A runner advertises capabilities, publishes their schemas to the kernel directory, and services job assignments. Tools are the same `@tool` decorator used by agents — the runner picks them up from the global registry.

```python
from rebuno import Runner, tool

@tool("compute.heavy")
async def heavy(data: str) -> str:
    """Run on a runner."""
    return await do_work(data)

Runner("compute-1").run()
```

Schemas for `@tool` functions are auto-derived from `inspect.signature` via pydantic. `Annotated[X, Field(description=...)]` flows through to JSON Schema descriptions, so the LLM (on the agent side) gets useful per-parameter docs.

### Hosting an MCP server on a runner

Use `runner.host()` to back tools with an MCP transport. The runner connects to MCP, lists tools, registers their schemas with the kernel, and forwards invocations.

```python
from rebuno import Runner, MCPServer

runner = Runner("github-runner")
runner.host(MCPServer(
    "github",
    url="https://api.githubcopilot.com/mcp/",
    headers={"Authorization": "Bearer xxx"},
))
runner.run()
```

Agents consume those tools via `remote.Tools("github")`. Credentials never leave the runner.

### Mixing `@tool` and MCP on a runner

Allowed. The runner publishes the union of both. **Hard error at startup if any tool ID collides** — pick one source per tool ID.

## Client (external services)

For services that aren't agents (Discord bots, GitHub webhooks, dashboards), `Client` is the HTTP API:

```python
from rebuno import Client

client = Client()  # env-configured

# Fire and forget
ex = await client.create("swe", input={"prompt": "..."})

# Stream events until terminal
async for event in client.run("swe", input={"prompt": "..."}):
    print(event.tool_id)

# Convenience: create + stream + return final state
result = await client.run_until_complete("swe", input={"prompt": "..."}, on_event=cb)
```

| Method | Description |
|---|---|
| `await client.create(agent_id, input=, labels=)` | Create an execution |
| `await client.get(id)` | Fetch current state |
| `await client.list(...)` | List executions with filters |
| `await client.cancel(id)` | Cancel a running execution |
| `await client.send_signal(id, type, payload=)` | Send a signal |
| `async for event in client.events(id)` | Stream events (terminates on terminal event) |
| `async for event in client.run(agent_id, input=)` | Create + stream |
| `await client.run_until_complete(...)` | Create + stream + return final `Execution` |
| `await client.list_tools(prefix=)` | Read the kernel tool directory |

## Timeouts

Step timeouts are controlled by policy rules. When a policy rule includes `timeout_ms` in its `then` block, that value is used as the step deadline. Otherwise the global `StepTimeout` (default: 5 min) applies. See [Policy](../policy.md) for details.
