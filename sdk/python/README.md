# Rebuno Python SDK

Python client library for the [Rebuno](https://github.com/rebuno/rebuno) agent execution runtime.

## Installation

```bash
pip install rebuno
```

## Quick Start

```python
from rebuno import RebunoClient

client = RebunoClient(base_url="http://localhost:8080")

# Create an execution
result = client.create_execution(agent_id="my-agent", input={"task": "hello"})
print(result["execution_id"])
```

## Building an Agent

```python
from rebuno.agent import BaseAgent

class MyAgent(BaseAgent):
    def process(self, ctx):
        result = ctx.invoke_tool("web.search", {"query": "hello"})
        return {"answer": result}

agent = MyAgent(
    agent_id="my-agent",
    kernel_url="http://localhost:8080",
)
agent.run()
```

## Building a Runner

```python
from rebuno.runner import BaseRunner

class MyRunner(BaseRunner):
    def execute(self, tool_id, arguments):
        if tool_id == "web.search":
            return {"results": ["..."]}
        raise ValueError(f"Unknown tool: {tool_id}")

runner = MyRunner(
    runner_id="my-runner",
    kernel_url="http://localhost:8080",
    capabilities=["web.search"],
)
runner.run()
```

## MCP Support

Connect to MCP servers to expose their tools through the kernel:

```bash
pip install rebuno[mcp]
```

```python
import asyncio
from rebuno import AsyncBaseRunner

runner = AsyncBaseRunner(
    runner_id="mcp-tools",
    kernel_url="http://localhost:8080",
)
runner.mcp_server(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)
asyncio.run(runner.run())
```

Agents can also use MCP tools as local tools:

```python
agent.mcp_server("filesystem", command="npx", args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"])
```

See the [full documentation](https://github.com/rebuno/rebuno/tree/main/docs) for details on partial failure tolerance, config-based setup, and runner MCP routing.

## Documentation

See the [full documentation](https://github.com/rebuno/rebuno/tree/main/docs).

## License

MIT
