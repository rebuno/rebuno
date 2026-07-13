# Getting started

## Install

```bash
pip install rebuno
```

Requires Python 3.11+. The SDK pulls in `httpx`, `pydantic` (v2), `fastapi`,
and `uvicorn` — enough to both host an agent and act as a client.

## Configuration

Every entry point reads from constructor arguments first and falls back to
environment variables, so the same code runs locally and in production without
edits:

| Variable | Used by | Purpose |
|----------|---------|---------|
| `REBUNO_URL` | `Agent`, `Client` | kernel base URL |
| `REBUNO_AGENT_SECRET` | `Agent` | HMAC secret shared with the kernel; signs every request and verifies inbound webhooks |
| `REBUNO_API_KEY` | `Client` | Bearer token for client/admin routes |

```python
# explicit
agent = Agent("dev-agent", secret="dev-secret", kernel_url="http://localhost:8080")

# from the environment (REBUNO_URL + REBUNO_AGENT_SECRET)
agent = Agent("dev-agent")
```

## The loop

There are two processes, and they talk to each other only through the kernel.

```
  your backend                 kernel                    your agent process
 ┌────────────┐          ┌──────────────┐              ┌──────────────────┐
 │  Client    │  create  │              │   webhook    │  Agent.run()     │
 │  .create() │ ───────► │  executions  │ ───────────► │  → your handler  │
 │            │          │  + steps     │ ◄─────────── │  → @tool /        │
 │  .get()    │ ◄─────── │  (durable)   │  submit_step │    http_client /  │
 └────────────┘  status  └──────────────┘   complete   │    step()        │
                                                        └──────────────────┘
```

1. A **client** calls `client.create(agent_id, input=...)`. The kernel records a
   new execution and dispatches it by POSTing a signed webhook to your agent.
2. Your **agent** verifies the signature, looks up the execution, and runs your
   handler. Each effect the handler performs is submitted to the kernel as a
   step *before* it runs — the kernel decides whether it proceeds, replays a
   recorded result, is denied by policy, or must wait for approval.
3. When the handler returns, the agent reports the output and the execution
   completes. If the handler blocked on an approval or crashed, the kernel
   re-dispatches later and the handler **replays** its recorded steps to get
   back to where it left off.

## A complete example

The agent process — hosts the handler and records its effects:

```python
# agent.py
from rebuno import Agent, tool


@tool
async def search(query: str) -> list[str]:
    return [f"result for {query}"]


async def process(prompt: str) -> dict:
    hits = await search(prompt)
    return {"answer": hits}


agent = Agent("dev-agent", secret="dev-secret", kernel_url="http://localhost:8080")
agent.run(process, port=5000)  # blocks, serving the webhook
```

A client that kicks off an execution and reads the result:

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

Run the agent in one terminal (it blocks, serving the webhook), then create an
execution from another:

```bash
python agent.py     # terminal 1
python client.py    # terminal 2
```

The kernel itself is a separate service — point `REBUNO_URL` / `kernel_url` at
wherever it runs. Next: [Agents](agents.md).
