# LLM calls

Rebuno records LLM calls as `llm_call` steps without you rewriting how you call
the model. `rebunoFetch` is a `fetch`-compatible function you hand to your
provider client or framework:

```ts
import { rebunoFetch } from "rebuno";
import { createOpenAI } from "@ai-sdk/openai";

const openai = createOpenAI({ fetch: rebunoFetch });
```

Anything that accepts a custom `fetch` works the same way.

## How it works

`rebunoFetch` sits between the provider client and the network, so every request
passes through it.

With no active execution it is a plain passthrough. The same function is safe to
use outside a handler, it just isn't durable there.

Inside an execution it reads the model id from the request body and records the
call as an `llm_call` step, on the same identity and replay machinery as tool
calls (see [How it works](internals.md)).

- On the first run it forwards the request, reads the response, and records
  `{status, headers, body}` as the step result. An error status is recorded like
  any other, so a recorded `500` replays as that same `500`. The provider
  client's own retry of a `500` is a fresh request, and records a separate step.
- On resume it rebuilds a `Response` from the recorded one. The provider is
  never called again, so a replay doesn't pay for the model twice.

The provider client parses the rebuilt response as if it came off the wire, so
your call site is unchanged. Only the status, content-type, and body are
reconstructed. Length and encoding headers are dropped so a replayed body is
never mismatched against a stale `content-length`.

Only string JSON request bodies are recognized as LLM calls. Anything else, such
as a stream or form body, passes through untouched.

## Streamed responses

A `text/event-stream` response is teed. The transport passes the provider's
bytes to your code as they arrive, accumulates the whole, and records it as the
step result when the stream ends. Deltas also go to the kernel's live side
channel as they arrive, so observers can watch the call run. See
[live streaming](../../streaming.md).

A stream that errors mid-flight fails the step instead of recording a truncated
response, and a consumer that cancels before EOF still records what arrived.

## Refused calls

A step the kernel doesn't allow to proceed comes back as an HTTP `403` or `429`
rather than an exception. An error thrown inside `fetch` would unwind through
the provider client, which retries unknown errors and rewraps them. A status
instead becomes the client's own error, which every framework propagates
untouched.

`Agent` recovers the refusal at the handler boundary even if your code never
looks at the error. A denial fails the execution with the kernel's reason. A
blocked call leaves the execution parked. Call `raiseForRefusal()` yourself to
unwind at your own boundary instead of running the rest of the handler first:

```ts
import { raiseForRefusal } from "rebuno";

try {
  const result = await generateText({ model: openai("gpt-4o"), prompt });
} catch (e) {
  raiseForRefusal(e);  // re-throws Blocked / PolicyError / RateLimited
  throw e;
}
```

Any error without the `rebuno_refusal` marker is left alone. An upstream
Rebuno-aware LLM gateway emits the same marker, so the same call covers it.

## Options

`rebunoFetch` runs over the global `fetch`. Use `createRebunoFetch` to wrap a
different one, to keep a custom proxy or retry config:

```ts
import { createRebunoFetch } from "rebuno";

const myFetch = createRebunoFetch({ fetch: undiciFetch });
```

The request body's `model` names the step `target`.
