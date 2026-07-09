# Python SDK

```bash
pip install rebuno
```

## Writing an agent

```python
from rebuno import Agent, tool

@tool
async def reverse(text: str) -> dict:
    return {"reversed": text[::-1]}

async def process(query: str) -> dict:
    result = await reverse(text=query)
    return {"query": query, "reversed": result["reversed"]}

if __name__ == "__main__":
    agent = Agent(
        "hello",
        secret="hello-secret",
        kernel_url="http://localhost:8080",
    )
    agent.run(process, port=5000)
```

### `Agent(agent_id, *, secret, kernel_url)`

The dispatch entrypoint. It runs a small HTTP server that receives the kernel's
signed webhooks, verifies the HMAC, binds an execution context, and runs your
`process` function — identically on first dispatch and on every resume.

`agent.run(process, port=...)` starts serving. `process` receives the execution's
input and returns its output. Effects inside it — `@tool` calls and LLM calls
routed through [`http_client()`](#http_client--durable-llm-calls) — short-circuit
on replay; your code can't tell whether a call ran for real or returned a recorded
result. See [agents.md](../agents.md).

### `@tool` decorator

```python
@tool                                  # target = function name
async def reverse(text: str) -> dict: ...

@tool("web_search")                    # explicit target
async def search(query: str) -> dict: ...

@tool("shell_exec", idempotency="at_most_once")
async def shell_exec(command: str) -> dict: ...
```

Each call becomes a recorded `tool_call` step. `idempotency` is `safe_to_retry`
(default) or `at_most_once` — it controls recovery when a crash orphans the step.
See [tools.md](../tools.md).

### `http_client()` — durable LLM calls

Unlike `@tool` calls, LLM calls are only durable if you route them through
Rebuno. `http_client()` returns an `httpx.AsyncClient` that records each LLM
request as an `llm_call` step; on resume the recorded response replays instead
of hitting the provider again. **Without it, LLM calls are not intercepted and
re-run on every resume** — burning tokens and breaking determinism.

Pass it to your LLM client's async HTTP client argument:

```python
from rebuno import http_client
from langchain_openai import ChatOpenAI

# LangChain
llm = ChatOpenAI(model="gpt-4o-mini", http_async_client=http_client())

# OpenAI SDK directly
from openai import AsyncOpenAI
llm = AsyncOpenAI(http_client=http_client())
```

`model_field` names the request-body field holding the model id (used as the
step `target`); it defaults to `"model"`. Streaming responses (`stream=True`)
are not recorded yet — they pass through and are **not** durable.

## Driving executions (client)

```python
from rebuno import Client

async with Client(base_url="http://localhost:8080", api_key="") as client:
    execution = await client.create("researcher", input={"query": "hello"})

    after_seq = 0
    while True:
        events = await client.events(execution.id, after_seq=after_seq)
        for evt in events:
            after_seq = evt.event_seq
            print(evt.type, evt.payload)
            if evt.type == "approval.requested":
                await client.grant_approval(evt.payload["approval_id"], decided_by="me")
        final = await client.get(execution.id)
        if final.status.is_terminal():
            print(final.output)
            break
```

| Method | Purpose |
|--------|---------|
| `create(agent_id, *, input)` | Create an execution; returns it (with `.id`, `.status`). |
| `get(id)` | Fetch current status, `output`, `failure_reason`. |
| `events(id, *, after_seq=0)` | Page the event log (`.event_seq`, `.type`, `.payload`, `.occurred_at`). |
| `grant_approval(approval_id, *, decided_by, rationale=None)` | Resolve an approval as granted. |
| `deny_approval(approval_id, *, decided_by, rationale=None)` | Resolve an approval as denied. |

`api_key` is the kernel's bearer token; leave it empty against the dev kernel. See
[`examples/python/client.py`](../../examples/python/client.py) for a full
interactive client that streams events and prompts for approvals.
