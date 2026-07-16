# Agents

`rebuno.Agent` is the runtime that hosts your handler. It serves an HTTP webhook,
receives dispatches from the kernel, and runs your handler once per dispatch with
an active [execution context](internals.md) so that `@tool`, `http_client()`, and
`step()` record durably.

```python
from rebuno import Agent

agent = Agent(
    "dev-agent",                      # agent_id (required, non-empty)
    secret="dev-secret",              # or REBUNO_AGENT_SECRET
    base_url="http://localhost:8080",  # or REBUNO_URL
    webhook_path="/webhook",          # default
    kernel_timeout=35.0,              # default; timeout for agent→kernel calls
)
```

## The handler

Your handler is any async function. **Its signature is the input schema** — how
you declare the parameters decides how an execution's `input` is delivered.
There are three shapes:

```python
# 1. keyword fields — each parameter is an input field
async def process(prompt: str, limit: int = 10) -> dict: ...
#    input={"prompt": "hi"}      → process(prompt="hi")
#    parameters without a default are required; a missing one fails the execution

# 2. a single pydantic model — input is validated against it
class In(BaseModel):
    prompt: str
    limit: int = 10
async def process(data: In) -> dict: ...
#    input={"prompt": "hi"}      → process(data=In(prompt="hi"))

# 3. raw passthrough — a single dict/Any/unannotated parameter gets input unchanged
async def process(input: dict) -> dict: ...
#    input={"anything": ...}     → process(input={"anything": ...})
```

Binding happens *before* your handler runs. A validation failure (missing
required field, or a pydantic error) fails the execution with a clear message and
your handler is never called. The return value becomes the execution's `output`
and must be JSON-serializable.

## `run()` vs `app`

The simple path binds the handler and serves it with uvicorn. This **blocks**:

```python
agent.run(process, host="0.0.0.0", port=5000)
```

To mount the agent into an existing service, or serve it with your own
uvicorn/gunicorn setup, use `agent.app` — a `FastAPI` instance with the webhook
route already registered:

```python
agent.bind(process)          # attach the handler
app = agent.app              # hand this to your ASGI server
```

`agent.app`'s lifespan closes the kernel HTTP client on shutdown, on the same
event loop that opened it. `agent.run(...)` does the equivalent cleanup itself.

## Dispatch and resume

Each webhook POST carries an `execution_id`. The agent:

1. **Verifies the signature.** The body is HMAC-SHA256'd with the agent secret
   and compared against the `Rebuno-Signature: sha256=...` header. A bad or
   missing signature returns `401`; a body with no `execution_id` returns `400`.
2. **Acknowledges immediately.** The handler runs in a background task and the
   webhook returns `200` right away, so the kernel's delivery isn't held open for
   the whole execution.
3. **Skips terminal executions.** If the execution is already
   `completed`/`failed`/`cancelled`, there's nothing to do.
4. **Hydrates and runs.** It loads the execution's already-terminal steps in one
   read (so replay is local, not a round trip per step), sets the ambient
   execution context, binds the input, and runs your handler.

Because the same handler runs on every dispatch, **resume is just re-running with
replay**: each recorded step returns its stored result instead of executing
again, so the handler fast-forwards to where it left off. You don't write resume
logic. See [How it works](internals.md) for the identity and replay mechanics
that make this safe — and why non-determinism outside a recorded step will break
it.

## What happens on failure

The agent maps outcomes from your handler onto the execution:

| Outcome | Effect |
|---------|--------|
| returns normally | execution **completes** with the return value as output |
| raises `Blocked` / `Terminated` | internal control-flow signals (an approval is pending, or the execution is terminal) — the dispatch unwinds cleanly and returns `200`; not an error |
| raises `PolicyError`, `ToolError`, `RateLimited`, `StepIDMismatch` | execution is **failed** with the message |
| raises any other exception | logged, execution is **failed** with the message |

See [Errors](errors.md) for what each exception means.

## Lifecycle

```python
await agent.join()   # await all in-flight execution handlers (best-effort)
await agent.close()  # cancel in-flight handlers and close the kernel client
```

`run()` and the `app` lifespan call `close()` for you. Call these directly only
when you manage the agent's lifetime yourself.
