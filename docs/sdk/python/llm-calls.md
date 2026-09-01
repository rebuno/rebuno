# LLM calls

Rebuno records LLM calls as `llm_call` steps without you rewriting how you call
the model. `rebuno.http_client()` returns an `httpx2.AsyncClient` you hand to your
provider's async client:

```python
from openai import AsyncOpenAI
import rebuno

llm = AsyncOpenAI(http_client=rebuno.http_client())
```

Any provider SDK built on `httpx2` that accepts a custom client works the same
way, such as Anthropic's `AsyncAnthropic(http_client=...)`.

`httpx2` is a separate package from `httpx`, and the two client types are not
interchangeable. A provider SDK still on `httpx` rejects the client. For the
OpenAI SDK that means version 3.0 or later, or 2.48 and later installed as
`openai[httpx2]`.

For an SDK with no `httpx2` support, `httpx2.alias_httpx()` makes `import httpx`
resolve to `httpx2` for the whole process, so the client passes the SDK's type
check. Call it from your entrypoint before anything imports `httpx`:

```python
import httpx2

httpx2.alias_httpx()
```

It rebinds `httpx` for every dependency in the process, so anything relying on
an `httpx` API that `httpx2` dropped breaks with it.

## How it works

`http_client()` installs `RebunoTransport` under the provider SDK, so `httpx2`
routes every request through it on the way to the network.

With no active execution the transport is a plain passthrough. The same client
is safe to use outside a handler, it just isn't durable there.

Inside an execution it reads the model id from the request body and records the
call as an `llm_call` step, on the same identity and replay machinery as tool
calls (see [How it works](internals.md)).

- On the first run it forwards the request, reads the response, and records
  `{status, headers, body}` as the step result. An error status is recorded like
  any other, so a recorded `500` replays as that same `500`. The provider SDK's
  own retry of a `500` is a fresh request, and records a separate step.
- On resume it rebuilds an `httpx2.Response` from the recorded one. The provider
  is never called again, so a replay doesn't pay for the model twice.

The provider SDK parses the rebuilt response as if it came off the wire, so your
call site is unchanged. Only the status, content-type, and body are
reconstructed. Length and encoding headers are dropped so a replayed body is
never mismatched against a stale `content-length`.

Only JSON request bodies are recognized as LLM calls. A non-JSON body such as a
file upload or a form post passes through untouched.

## Streamed responses

A `text/event-stream` response is teed. The transport passes the provider's
bytes to your code as they arrive, accumulates the whole, and records it as the
step result when the stream ends. Deltas also go to the kernel's live side
channel as they arrive, so observers can watch the call run. See
[live streaming](../../streaming.md).

A replayed streamed call is delivered as a stream too, so the provider SDK
iterates it the same way. A stream that errors mid-flight fails the step instead
of recording a truncated response.

## Refused calls

A step the kernel doesn't allow to proceed comes back as an HTTP `403` or `429`
rather than an exception. An exception raised inside the transport would unwind
through the provider SDK, which retries unknown exceptions and rewraps them as
`APIConnectionError`. A status instead becomes the SDK's own error, such as
`openai.PermissionDeniedError`, which every framework propagates untouched.

`Agent` recovers the refusal at the handler boundary even if your code never
looks at the error. A denial fails the execution with the kernel's reason. A
blocked call leaves the execution parked. Call `rebuno.raise_for_refusal()`
yourself to unwind at your own boundary instead of running the rest of the
handler first:

```python
try:
    resp = await llm.chat.completions.create(...)
except Exception as e:
    rebuno.raise_for_refusal(e)  # re-raises Blocked / PolicyError / RateLimited
    raise
```

Any error without the `rebuno_refusal` marker is left alone. An upstream
Rebuno-aware LLM gateway emits the same marker, so the same call covers it.

## Options

```python
rebuno.http_client(
    timeout=30.0,          # any extra kwargs are forwarded to httpx2.AsyncClient
)
```

The request body's `model` names the step `target`.

You can also construct the transport directly, wrapping an existing one to keep
a custom proxy or retry config:

```python
import httpx2
from rebuno import RebunoTransport

transport = RebunoTransport(httpx2.AsyncHTTPTransport())
llm = AsyncOpenAI(http_client=httpx2.AsyncClient(transport=transport))
```
