# Tools

A tool is an action your agent takes. Wrapping a function as a tool routes every
call through the kernel, so it's recorded as a `tool_call` step — subject to
policy, replayed on resume, and visible in the execution's audit trail.

## `@tool`

Decorate an async function:

```python
from rebuno import tool


@tool
async def search(query: str, limit: int = 10) -> list[str]:
    ...
```

The wrapper keeps the original signature (via `functools.wraps`), so frameworks
that introspect your function bind it unchanged. Hand tools to your agent
framework as a plain list:

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

`idempotency` controls whether a tool may re-run when a dispatch replays:

- **`safe_to_retry`** (default) — reads and other operations that are fine to
  execute again. On resume, the recorded result replays; if the step never
  completed, it can run again.
- **`at_most_once`** — destructive or non-idempotent operations that must not be
  repeated (sending an email, charging a card). The kernel guarantees the effect
  body runs at most once even across crashes.

```python
@tool(idempotency="at_most_once")
async def send_email(to: str, body: str) -> None:
    ...
```

### Blocking work

The event loop must stay responsive — the kernel lease is renewed by a
background heartbeat that only fires if the effect body yields (see
[How it works](internals.md)). Offload blocking or CPU-bound work to a thread:

```python
@tool
async def render(doc: str) -> bytes:
    return await asyncio.to_thread(render_sync, doc)
```

### Calling context

A tool records against the current execution. Calling one outside an active
dispatch (i.e. not inside a handler running under `agent.run()` or a test
context) raises `RuntimeError`.

## `wrap_tool`

`@tool` fits plain functions. `wrap_tool` builds a Rebuno-routed callable from a
`name` plus an `invoke(args)` seam, for tools that aren't plain callables —
framework tool objects, or schema-only tools:

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

- `name` is the tool id the LLM (via `__name__`) *and* the kernel see — put any
  namespace prefix directly in it.
- `args_schema`'s `properties`/`required` build a keyword-only signature that
  frameworks introspect, and is exposed on `__input_schema__`. The wrapper still
  accepts `**kwargs`, so an argument outside the schema is passed through.
- `to_result` maps the raw return before it's recorded (default: identity).
- `transform_args` maps the argument dict before it's recorded and passed to
  `invoke` (default: identity) — e.g. null-stripping.

## MCP tools

`rebuno.mcp` wraps [Model Context Protocol](https://modelcontextprotocol.io)
tool descriptors, so tools served over MCP get the same durability and policy as
native ones.

```python
from rebuno.mcp import wrap_mcp_tools

tools = wrap_mcp_tools(
    await session.list_tools(),        # descriptors with name/description/inputSchema
    call=session.call_tool,            # call(tool_name, args) → result
    prefix="docs",                     # tool id becomes f"{prefix}_{name}"
)
agent = create_agent(llm, tools)
```

- Descriptors can be attribute-style (the official `mcp` SDK's `Tool`, fastmcp
  tools) or plain dicts — both work.
- `prefix` namespaces the tool id: the LLM and kernel see `f"{prefix}_{name}"`,
  while the MCP server (via `call`) sees the bare `name`. Empty prefix uses the
  name as-is.
- By default the result is flattened from a standard MCP `CallToolResult`
  (structured content if present, otherwise joined text blocks), and `null`
  arguments are stripped (LLMs often fill optional fields with `null`, which
  typed MCP parameters reject). Override with `to_result` / by wrapping yourself.

`wrap_mcp_tool` does one descriptor; `wrap_mcp_tools` maps over a list.
