# LLM calls

LLM calls are the most expensive and least deterministic thing an agent does, so
Rebuno records them too — as `llm_call` steps — without you rewriting how you call
the model. `rebuno.http_client()` returns an `httpx.AsyncClient` you hand to your
provider's async client:

```python
from openai import AsyncOpenAI
import rebuno

llm = AsyncOpenAI(http_client=rebuno.http_client())
```

Any provider SDK built on `httpx` and accepting a custom client works the same
way (e.g. Anthropic's `AsyncAnthropic(http_client=...)`).

## How it works

`http_client()` installs `RebunoTransport` under the provider SDK. `httpx` routes
every request through the transport, which sits between the provider client and
the network:

1. If there is **no active execution**, it's a plain passthrough — the request
   goes straight to the provider. (So the same client is safe to use outside a
   handler; it just isn't durable there.)
2. Inside an execution, it reads the model id from the request body and records
   the call as an `llm_call` step (the same identity/replay machinery as tool
   calls — see [How it works](internals.md)):
   - **First run:** it forwards the request to the provider, reads the full
     response, and records `{status, headers, body}` as the step result.
   - **Resume:** it returns the *recorded* response and rebuilds an
     `httpx.Response` from it — the provider is never called again, so a replayed
     dispatch doesn't re-pay for the model.

The provider SDK parses the rebuilt response exactly as if it came off the wire,
so your `llm.chat.completions.create(...)` call site is unchanged.

## Options

```python
rebuno.http_client(
    model_field="model",   # request-body field holding the model id (the step target)
    timeout=30.0,          # any extra kwargs are forwarded to httpx.AsyncClient
)
```

The model id names the step `target`. Most providers use `model`; set
`model_field` if yours differs.

You can also construct the transport directly and wrap an existing transport
(e.g. to keep a custom proxy or retry config):

```python
import httpx
from rebuno import RebunoTransport

transport = RebunoTransport(httpx.AsyncHTTPTransport(), model_field="model")
llm = AsyncOpenAI(http_client=httpx.AsyncClient(transport=transport))
```

## Current limits

- **Streaming durability** is provided by the kernel's live side channel — the
  interceptor tees the provider stream, records the assembled whole via
  `.../complete`, and republishes live deltas to `.../stream`. See
  [live streaming](../../streaming.md).
- **Only JSON request bodies are recognized** as LLM calls. Non-JSON bodies
  (file uploads, form posts) pass through untouched.

When a response is replayed, only status, content-type, and body are
reconstructed — hop-by-hop and length/encoding headers are dropped so a replayed
body is never mismatched against a stale `content-length` or `content-encoding`.
