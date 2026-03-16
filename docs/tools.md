# Tools

Tools are functions that agents invoke through the kernel. Every tool call goes through policy enforcement and is recorded in the event log. Tools can execute locally (in the agent process) or remotely (in a runner process).

## Local vs Remote

| | Local | Remote |
|---|---|---|
| Latency | Lower (no network hop) | Higher (kernel -> runner -> kernel) |
| Isolation | Runs in agent process | Separate process, can have different dependencies |
| Scaling | Scales with agent | Scales independently |
| Simplicity | Simpler setup | Requires runner process |

Use **local tools** when the tool is lightweight and doesn't need isolation. Use **remote tools** when you need process isolation, different runtime dependencies, or independent scaling.

See [Python SDK](sdk.md) for how to register and use both types.

## Tool IDs

Tool IDs are dot-separated strings (e.g., `web.search`, `doc.fetch`, `shell.exec`). Policy rules can use glob patterns to match groups of tools:

```yaml
when:
  tool_ids: ["web.*"]   # matches web.search, web.fetch, etc.
```

## MCP Tools

MCP servers can be connected to agents and runners, making their tools available through the kernel with full policy enforcement and audit logging.

MCP tools are namespaced with the server name as prefix (e.g., `filesystem.read_file`, `github.list_repos`). Policy rules can match them with globs:

```yaml
when:
  tool_ids: ["filesystem.*"]
then: allow
```

MCP tools can be registered on either agents (as local tools) or runners (as remote capabilities). See [Python SDK > MCP Tools](sdk.md#mcp-tools) for setup.

### Partial failure and retry

`McpManager` tolerates partial connection failures. If some MCP servers fail to connect, the runner/agent starts with whichever servers succeeded. Failed servers are retried in the background, and when they reconnect, the runner automatically updates its capabilities with the kernel.

### Config-based setup

MCP servers can also be loaded from a config dict matching the standard `mcpServers` format:

```python
config = {
    "mcpServers": {
        "filesystem": {
            "command": "npx",
            "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
        },
        "api": {
            "url": "http://localhost:3000/mcp",
        },
    }
}
agent.mcp_servers_from_config(config)
```

## Execution Flow

```
Agent calls ctx.invoke_tool("web.search", {"query": "..."})
  -> SDK submits invoke_tool intent to kernel
  -> Kernel evaluates policy
  -> If denied: PolicyError raised, tool never executes
  -> If allowed: step created
     -> Local: SDK executes function, reports result to kernel
     -> Remote: kernel pushes job to runner via SSE, runner executes, reports result
  -> Result delivered back to agent
```

See [Python SDK](sdk.md) for the full agent and runner API. See [Policy](policy.md) for controlling tool access.
