# LLM calls

To the kernel an LLM call is just an effect. It is an `llm_call` step that travels
the same submission, replay, policy, and idempotency path as a
[tool call](tools.md). An LLM request is not something the agent issues by hand,
so it has to be intercepted before it can become durable.

## Why LLM calls need interception

A tool call is explicit in agent code, so wrapping it is straightforward. An LLM
call is an HTTP request buried inside a model provider's SDK or an agent
framework, and the agent never issues it directly. For that request to be durable,
something has to intercept it at the HTTP layer and put it through the step
contract like any other effect.

Without interception an LLM call is invisible to the kernel and re-runs on every
resume. That burns tokens, and because model output varies, it breaks the
determinism replay depends on.

## How an LLM call becomes a step

The interception point treats each outbound LLM request as an `llm_call` step and
submits it (`POST /v0/executions/{id}/steps`) exactly like a tool call:

- `target` is the model id, and the arguments are the request body.
- Identity is computed over the canonicalized arguments, so any field that varies
  between attempts varies the step ID with it. See
  [step identity](tools.md#step-identity).
- On `replay`, the recorded response is returned and no provider call happens. On
  `proceed`, the request goes to the provider and the response is recorded as the
  step outcome.
- An `llm_call` step defaults to `safe_to_retry`, so an orphaned call is re-run
  rather than failed as `indeterminate`.

Policy applies here too. An `llm_call` step is evaluated like any other effect, so
you can gate models or arguments with [policy](policy.md).

## Streamed responses

While a call is in flight, deltas can go to observers over an ephemeral side
channel. They never enter the step record, so a streamed call is recorded and
replayed as the whole assembled output. See [live streaming](streaming.md).

## Implement interception

The Rebuno SDKs ship an interceptor you drop into your model client, which makes
this transparent. See [Python SDK: LLM calls](sdk/python/llm-calls.md).

The contract is HTTP, so your own gateway can implement it without the SDK. For
each request:

1. Submit the step to `POST /v0/executions/{id}/steps` as `{kind: "llm_call",
   target: <model>, args: <request body>}`, with the caller's dispatch id in
   `Rebuno-Dispatch-Id`. The decision carries the `step_id`.
2. On `replay`, return the recorded response and skip the provider. On `proceed`,
   forward to the provider and record the response via `.../complete` (or
   `.../fail`).
3. On any other decision, refuse the request with `403`, or `429` for
   `rate_limited`, and a body of `{"error": {"type": "rebuno_refusal", "message":
   "rebuno_refusal: <decision>[ reason=<why>]"}}`. Provider SDKs only ever surface
   an error body as text, so the flat marker is what a caller's `raise_for_refusal`
   reads back to turn a `blocked` call into a parked execution rather than a failed
   one.

A gateway needs the dispatch id to reach it, so the agent passes it through on
each request. A header on the call into the gateway is enough. See
[`examples/gateway/litellm_proxy.py`](../examples/gateway/litellm_proxy.py) for a
LiteLLM gateway example, and the [HTTP API](api.md#agent-api) for the exact
request and response shapes.
