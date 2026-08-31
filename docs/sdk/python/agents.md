# Agents

`rebuno.Agent` serves an HTTP webhook and runs your handler once per dispatch.
It runs under an active [execution context](internals.md), so `@tool`,
`http_client()`, and `step()` record durably.

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

The handler is the async function you hand to `agent.run()` or `agent.bind()`,
named `process` in the examples below. Its signature is the input schema: the
parameters decide how an execution's `input` is delivered. Three shapes:

```python
# 1. keyword fields: each parameter is an input field
async def process(prompt: str, limit: int = 10) -> dict: ...
#    input={"prompt": "hi"}      → process(prompt="hi")
#    parameters without a default are required; a missing one fails the execution

# 2. a single pydantic model: input is validated against it
class In(BaseModel):
    prompt: str
    limit: int = 10
async def process(data: In) -> dict: ...
#    input={"prompt": "hi"}      → process(data=In(prompt="hi"))

# 3. raw passthrough: a single dict/Any/unannotated parameter gets input unchanged
async def process(input: dict) -> dict: ...
#    input={"anything": ...}     → process(input={"anything": ...})
```

Binding happens before your handler runs. A missing required field or a pydantic
error fails the execution. The return value becomes the execution's `output` and
has to be JSON-serializable.

## `run()` vs `app`

`run()` binds the handler and serves it with uvicorn. It blocks:

```python
agent.run(process, host="0.0.0.0", port=5000)
```

Use `agent.app` to mount the agent into an existing service, or to run your own
uvicorn/gunicorn. It is a `FastAPI` instance with the webhook route registered:

```python
agent.bind(process)          # attach the handler
app = agent.app              # hand this to your ASGI server
```

`agent.app`'s lifespan closes the kernel HTTP client on shutdown. `agent.run(...)`
does the equivalent cleanup itself.

## Dispatch and resume

Each webhook POST carries an `execution_id`, a `dispatch_id`, and a
`dispatch_attempt`. The agent:

1. Verifies the `Rebuno-Signature` header (see [Signing](internals.md#signing)).
   A bad or missing signature gets a `401`, and a body missing any of the three
   gets a `400`.
2. Acknowledges immediately. The handler runs in a background task and the
   webhook returns `200` right away, so delivery isn't held open for the whole
   execution.
3. Cancels the handler still running for that execution when the webhook
   supersedes it, so a re-delivery doesn't leave two copies racing. A repeat of
   the attempt already running, or of one the kernel has moved past, is ignored.
4. Skips terminal executions. Nothing to do if it is already `completed`,
   `failed`, or `cancelled`.
5. Runs. It fetches the execution's input, binds it, and calls your handler under
   the ambient execution context.

The same handler runs on every dispatch, and each recorded step returns its
stored result instead of executing again. See [How it works](internals.md) for
what breaks this.

A `Blocked` or `Terminated` your handler swallows still ends the dispatch
correctly, because the execution context remembers it and `Agent` re-raises it.
A denial or rate limit has no such backstop and has to escape the handler, but it
may arrive wrapped in a provider SDK's own error type, which
`raise_for_refusal()` unwraps.

## What happens on failure

The agent maps outcomes from your handler onto the execution:

| Outcome | Effect |
|---------|--------|
| returns normally | execution completes with the return value as output |
| raises `Blocked` or `Terminated` | control-flow signals. An approval is pending, or the execution is terminal. The dispatch unwinds and the kernel's state stands |
| raises `PolicyError` or `RateLimited` | execution is failed with the reason. `@tool` catches `PolicyError` and returns a string, so a denial reaches here only from an LLM call or a `step()`. `RateLimited` reaches here from any of them |
| raises `ToolError` | execution is failed with the message |
| raises any other exception | logged, and the execution is failed with the message |

See [Errors](errors.md) for what each exception means.

## Lifecycle

```python
await agent.join()   # await all in-flight execution handlers (best-effort)
await agent.close()  # cancel in-flight handlers and close the kernel client
```

`run()` and the `app` lifespan call `close()` for you. Call these directly only
when you manage the agent's lifetime yourself.
