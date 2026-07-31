# LLM calls

LLM calls are the most expensive and least deterministic thing an agent does, so
Rebuno records them too — as `llm_call` steps — without you rewriting how you call
the model. `rebunoFetch` is a `fetch`-compatible function you hand to your
provider's client (or your LLM framework):

```ts
import { rebunoFetch } from "rebuno";
import { createOpenAI } from "@ai-sdk/openai";

const openai = createOpenAI({ fetch: rebunoFetch });
```

Any provider SDK or framework that lets you inject a custom `fetch` works the same
way — the Vercel AI SDK's provider factories, the OpenAI/Anthropic SDKs, or a bare
`fetch` call you make yourself.

## How it works

`rebunoFetch` wraps `fetch`. Every request routes through it, and it sits between
your model client and the network:

1. If there is **no active execution**, it's a plain passthrough — the request
   goes straight to the provider. (So the same client is safe to use outside a
   handler; it just isn't durable there.)
2. Inside an execution, it reads the model id from the request body and records
   the call as an `llm_call` step (the same identity/replay machinery as tool
   calls — see [How it works](internals.md)):
   - **First run:** it forwards the request to the provider, reads the full
     response, and records `{ status, headers, body }` as the step result.
   - **Resume:** it returns the *recorded* response and rebuilds a `Response` from
     it — the provider is never called again, so a replayed dispatch doesn't
     re-pay for the model.

The provider SDK parses the rebuilt `Response` exactly as if it came off the wire,
so your `generateText(...)` / `chat.completions.create(...)` call site is
unchanged.

## Refused calls

A step the kernel doesn't allow to proceed — denied by policy, rate limited, or
parked awaiting approval — comes back as an HTTP `403`/`429` rather than a thrown
error, because an error thrown inside `rebunoFetch` would unwind through the
provider SDK, which retries unknown errors and rewraps them. The status becomes
the provider SDK's own error, which every framework propagates untouched.

That failure is already correct for a denial. To handle a *blocked* call — where
the execution should park for the approval, not fail — pass the provider's error
to `raiseForRefusal()` at whatever boundary your code owns:

```ts
import { raiseForRefusal } from "rebuno";

try {
  const resp = await generateText({ ... });
} catch (e) {
  raiseForRefusal(e); // rethrows Blocked / PolicyError / RateLimited
  throw e;
}
```

`Blocked` unwinds the handler and leaves the execution parked; granting the
approval dispatches it again, and every step recorded so far replays from the log.
Any error without the `rebuno_refusal` marker is left alone. An upstream
Rebuno-aware LLM gateway emits the same marker, so the same call covers it.

## Options

The default `rebunoFetch` reads the model id from the request body's `model`
field — it names the step `target` — and uses the global `fetch`. Use
`createRebunoFetch` to forward through a different one:

```ts
import { createRebunoFetch } from "rebuno";

const myFetch = createRebunoFetch({
  fetch: customFetch,    // inner fetch to forward through (default: global fetch)
});
```

That keeps a proxy, retry wrapper, or instrumented client underneath the
recording layer.

## Current limits

- **Streaming durability** is provided by the kernel's live side channel — the
  interceptor tees the provider stream, records the assembled whole via
  `.../complete`, and republishes live deltas to `.../stream`. See
  [live streaming](../../streaming.md).
- **Only string JSON request bodies are recognized** as LLM calls. A request
  whose body isn't a JSON string (a `ReadableStream`, `FormData`, a file upload)
  passes through untouched.

When a response is replayed, only status, content-type, and body are
reconstructed — other headers are dropped so a replayed body is never mismatched
against a stale `content-length` or `content-encoding`.
