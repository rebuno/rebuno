# Getting started

## Install

```bash
pip install rebuno
```

Requires Python 3.11 or later. The SDK pulls in `httpx`, `pydantic` (v2),
`fastapi`, and `uvicorn`.

## Configuration

Every entry point takes constructor arguments first, then environment variables.

| Variable | Used by | Purpose |
|----------|---------|---------|
| `REBUNO_URL` | `Agent`, `Client` | kernel base URL |
| `REBUNO_AGENT_SECRET` | `Agent` | HMAC secret shared with the kernel; signs every request and verifies inbound webhooks |
| `REBUNO_API_KEY` | `Client` | Bearer token for client routes |

```python
# explicit
agent = Agent("dev-agent", secret="dev-secret", base_url="http://localhost:8080")

# from the environment (REBUNO_URL + REBUNO_AGENT_SECRET)
agent = Agent("dev-agent")
```

## The loop

Your backend and your agent communicate through the kernel.

```mermaid
flowchart LR
    c["Client<br>your backend"] -->|"create, get"| k[kernel]
    k -->|webhook| a["Agent<br>your handler"]
    a -->|"submit_step, complete"| k
```

1. A client calls `client.create(agent_id, input=...)`. The kernel records the
   execution and POSTs a signed webhook to your agent.
2. Your agent verifies the signature, looks up the execution, and runs your
   handler. Each effect goes to the kernel as a step before it runs. The kernel
   decides whether it proceeds, replays a recorded result, is denied by policy,
   or waits for approval.
3. When the handler returns, the agent reports the output and the execution
   completes. If it blocked on an approval or crashed, the kernel re-dispatches
   later and the recorded steps replay.

## A complete example

```python
# agent.py
from rebuno import Agent, tool


@tool
async def search(query: str) -> list[str]:
    return [f"result for {query}"]


async def process(prompt: str) -> dict:
    hits = await search(prompt)
    return {"answer": hits}


agent = Agent("dev-agent", secret="dev-secret", base_url="http://localhost:8080")
agent.run(process, port=5000)  # blocks, serving the webhook
```

```python
# client.py
import asyncio
from rebuno import Client


async def main() -> None:
    async with Client(base_url="http://localhost:8080") as client:
        ex = await client.create("dev-agent", input={"prompt": "hello"})
        print(await client.get(ex.id))


asyncio.run(main())
```

## Running locally

```bash
python agent.py     # terminal 1
python client.py    # terminal 2
```

The kernel is a separate service. Point `REBUNO_URL` or `base_url` at it.
Next: [Agents](agents.md).
