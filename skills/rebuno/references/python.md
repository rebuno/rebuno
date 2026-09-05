# Python

Requires Python 3.11+. Add `rebuno` with the project's package manager
(e.g. `uv add rebuno` or `python -m pip install rebuno`).

## Starter

With the setup guide's environment and registration, save as `agent.py` and
run `python agent.py`:

```python
from rebuno import Agent, tool


@tool("word_count")
async def word_count(text: str) -> dict:
    """Count words in text."""
    return {"count": len(text.split())}


async def handle(query: str) -> dict:
    return {"result": await word_count(text=query)}


if __name__ == "__main__":
    Agent("my-agent").run(handle, host="127.0.0.1", port=5000)
```

This starter records a tool call; add the user's actual model loop when requested.
Handler keyword parameters come from execution input. A single Pydantic model
validates input; a single `dict` parameter receives it directly. Return JSON data.

## Existing applications

- **Server:** `agent.bind(handle)` attaches the handler; `agent.app` is its
  FastAPI app. Mount/serve it through ASGI, include the mount prefix in the
  webhook URL, and close the agent in the parent's lifespan if needed.
- **Functions:** `search = tool("search")(existing_search)` wraps an existing
  async callable. For non-idempotent writes add `idempotency="at_most_once"`.
  Pass the wrapper to the framework's actual execution callback.
- **Tool objects:** `wrap_tool(name, invoke=lambda args: existing.ainvoke(args),
  args_schema=schema)` adapts an invocation API. `to_result` serializes output;
  `transform_args` changes arguments before both policy evaluation and invocation.
- **MCP:** `rebuno.mcp.wrap_mcp_tools` takes descriptors and a
  `call(tool_name, args)` adapter. `prefix` produces targets `<prefix>_<name>`.
  See [tool adapters](https://github.com/rebuno/rebuno/blob/main/docs/sdk/python/tools.md)
  for schema and session details.

## Model interception

Pass Rebuno's client to the provider actually used inside the bound handler:

```python
from openai import AsyncOpenAI
from rebuno import http_client

llm = AsyncOpenAI(http_client=http_client())
```

Compatible LangChain `ChatOpenAI` uses `http_async_client=http_client()`.
Preserve existing provider options. The SDK documented here returns an
`httpx2.AsyncClient`; verify provider compatibility with that type. Older
`httpx` clients are not interchangeable. Avoid automatically applying the
process-wide `httpx2.alias_httpx()`.

The transport records JSON requests using `model` as the target. Outside an
execution, or for non-JSON requests, it passes through without recording.
For custom transports and streaming, consult
[LLM calls](https://github.com/rebuno/rebuno/blob/main/docs/sdk/python/llm-calls.md).

## Runtime details

- Tools and `step()` require an active dispatch. Tool denial returns a string
  even when the normal result is an object; denied `step()` raises `PolicyError`.
- At broad provider/framework exception handlers, call `raise_for_refusal(exc)`
  from `rebuno` before retry/fallback logic. Let `Blocked` and `Terminated` unwind.
- `step(name, fn, args=None, idempotency="safe_to_retry")` calls `fn(**args)`.
  For example, after importing `time` and `step`,
  `await step("timestamp", time.time)` records a stable value. Use JSON data.
- Offload blocking work with `asyncio.to_thread` so lease heartbeats run.
  Await effects within the handler's lifetime.

From async backend code, use `async with Client() as client:` and
`await client.create("my-agent", input={"query": "hello"})`. Creation returns
before completion; inspect with `client.get` and `client.list_steps`.
`Client` reads `REBUNO_URL` and optional `REBUNO_API_KEY`.
