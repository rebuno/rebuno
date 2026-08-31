# Tools

A tool is an action your agent takes. Wrapping a function as a tool routes every
call through the kernel, where it's recorded as a `tool_call` step. Policy
applies to it, it replays on resume, and it shows up in the audit trail.

## `@tool`

Decorate an async function:

```python
from rebuno import tool


@tool
async def search(query: str, limit: int = 10) -> list[str]:
    ...
```

The wrapper keeps the original signature (via `functools.wraps`), so frameworks
that introspect your function bind it unchanged. Hand tools to a framework as a
plain list:

```python
agent = create_agent(llm, [search, ...])
```

Forms of the decorator:

```python
@tool                                   # bare
@tool()                                 # called, same as bare
@tool("custom_id")                      # explicit tool id (default is the fn name)
@tool("charge", idempotency="at_most_once")   # id + idempotency
```

### Idempotency

`idempotency` controls whether a tool may run again when a dispatch replays.

- `safe_to_retry` (default) is for reads and anything else fine to execute
  again. On resume the recorded result replays, and a step that never completed
  runs again.
- `at_most_once` is for destructive operations such as sending an email or
  charging a card. If a resumed dispatch finds the step already executing, the
  kernel fails it with reason `indeterminate` and the SDK raises `ToolError`, so
  your handler decides how to reconcile. A step recorded but never started is
  still safe, so it runs.

```python
@tool(idempotency="at_most_once")
async def send_email(to: str, body: str) -> None:
    ...
```

### Denied calls

A tool denied by policy does not raise. `@tool` and `wrap_tool` catch the
`PolicyError` and return a string instead:

```
search not allowed. reason: <the rule's reason>
```

The LLM sees that as the tool's result and can pick a different path, rather
than the denial crashing the run. A denied `rebuno.step()` raises `PolicyError`
normally, since nothing reads its result as a tool response.

### Blocking work

The event loop has to stay responsive. The kernel lease is renewed by a
background heartbeat that only fires while the handler yields (see
[How it works](internals.md#heartbeats-and-leases)). Offload blocking or
CPU-bound work to a thread:

```python
@tool
async def render(doc: str) -> str:
    return await asyncio.to_thread(render_sync, doc)  # returns JSON-serializable
```

### Calling context

A tool records against the current execution. Calling one outside an active
dispatch raises `RuntimeError`. Active means inside a handler running under
`agent.run()`, or inside a test context.

## `wrap_tool`

`@tool` fits plain functions. `wrap_tool` builds a Rebuno-routed callable from a
`name` plus an `invoke(args)` seam. Use it for tools that aren't plain callables,
like framework tool objects or schema-only tools:

```python
from rebuno import wrap_tool

search = wrap_tool(
    "search",
    invoke=lambda args: my_client.search(**args),   # awaitable or plain return
    description="Search the corpus",                # shown to the LLM (__doc__)
    args_schema={                                    # builds the synthetic signature
        "properties": {"query": {"type": "string"}},
        "required": ["query"],
    },
    idempotency="safe_to_retry",
    to_result=None,        # map invoke's return to a JSON-serializable value
    transform_args=None,   # map the caller's arg dict before recording/invoking
)
```

- `name` is the tool id the LLM sees (via `__name__`) and the kernel sees, so put
  any namespace prefix directly in it.
- `args_schema`'s `properties` and `required` build a keyword-only signature that
  frameworks introspect. The schema itself is exposed on `__input_schema__`. The
  wrapper still accepts `**kwargs`, so an argument outside the schema is passed
  through.
- `to_result` maps the raw return before it's recorded. Defaults to identity.
- `transform_args` maps the argument dict before it's recorded and passed to
  `invoke`, for something like null-stripping. Defaults to identity.

## MCP tools

`rebuno.mcp` wraps [Model Context Protocol](https://modelcontextprotocol.io)
tool descriptors, so MCP tools get the same treatment as native ones.

```python
from rebuno.mcp import wrap_mcp_tools

tools = wrap_mcp_tools(
    await session.list_tools(),        # descriptors with name/description/inputSchema
    call=session.call_tool,            # call(tool_name, args) → result
    prefix="docs",                     # tool id becomes f"{prefix}_{name}"
)
agent = create_agent(llm, tools)
```

- Descriptors can be attribute-style (the official `mcp` SDK's `Tool`, a fastmcp
  tool) or plain dicts. Both work.
- `prefix` namespaces the tool id. The LLM and the kernel see `f"{prefix}_{name}"`,
  while the MCP server (via `call`) sees the bare `name`. An empty prefix uses
  the name as-is.
- The result is flattened from a standard MCP `CallToolResult` by default,
  preferring structured content and otherwise joining text blocks. Override it
  with `to_result`.
- Null arguments are stripped by default, since LLMs often fill optional fields
  with `null` and typed MCP parameters reject it.

`wrap_mcp_tool` does one descriptor; `wrap_mcp_tools` maps over a list.
