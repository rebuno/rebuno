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

## Options

The default `rebunoFetch` reads the model id from the request body's `model`
field and uses the global `fetch`. Use `createRebunoFetch` to override either:

```ts
import { createRebunoFetch } from "rebuno";

const myFetch = createRebunoFetch({
  modelField: "model",   // request-body field holding the model id (the step target)
  fetch: customFetch,    // inner fetch to forward through (default: global fetch)
});
```

The model id names the step `target`. Most providers use `model`; set
`modelField` if yours differs. Pass a custom `fetch` to keep a proxy, retry
wrapper, or instrumented client underneath the recording layer.

## Current limits

- **Streaming is not durable yet.** A request with `stream: true` in its body is
  passed through un-recorded (you get a `console.warn`). The call works, but it
  won't replay.
- **Only string JSON request bodies are recognized** as LLM calls. A request
  whose body isn't a JSON string (a `ReadableStream`, `FormData`, a file upload)
  passes through untouched.

When a response is replayed, only status, content-type, and body are
reconstructed — other headers are dropped so a replayed body is never mismatched
against a stale `content-length` or `content-encoding`.
