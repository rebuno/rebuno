# LLM calls

To the kernel an LLM call is just an effect — an `llm_call` step that travels the
same submission, replay, policy, and idempotency path as a [tool call](tools.md).
What makes it worth its own page is *how* it gets recorded: unlike a tool call, an
LLM request is not something the agent issues by hand, so it has to be intercepted
before it can become durable.

## Why LLM calls need interception

A tool call is explicit in agent code, so wrapping it is straightforward. An LLM
call is different: it is an HTTP request buried inside a model provider's SDK or an
agent framework, and the agent never issues it directly. For that request to be
durable it must be **intercepted at the HTTP layer** and put through the step
contract like any other effect.

Without interception an LLM call is invisible to the kernel and re-runs on every
resume — burning tokens and, because model output varies, breaking the determinism
that replay depends on.

## How it becomes a step

The interception point treats each outbound LLM request as an `llm_call` step and
submits it (`POST /v0/executions/{id}/steps`) exactly like a tool call:

- `target` is the model id; the arguments are the request body.
- Identity is computed over a **canonical form** of the request (messages, tools,
  model, sampling params) and excludes operational noise — request IDs, trace
  headers, the streaming flag. See [step identity](tools.md#step-identity).
- On `replay`, the recorded provider response is returned and **no provider call
  ever happens**. On `proceed`, the request is forwarded to the provider and the
  response is recorded as the step outcome — its `step.succeeded` event carries the
  response, token counts, and cost.

LLM calls are always `safe_to_retry`: the step ID doubles as the provider's
idempotency key, so a retried call after a crash deduplicates at the provider.

Policy applies here too — an `llm_call` step is evaluated like any other effect, so
you can gate models or arguments with [policy](policy.md).

## Implementing interception

The Rebuno SDKs ship an interceptor you drop into your model client, so this is
transparent — see the [Python SDK](sdk/python.md#http_client--durable-llm-calls).

**You don't need the SDK for it, though — the contract is just HTTP.** If you
already run your own LLM gateway or proxy in front of your providers, implement the
same steps there. For each request:

1. Compute the step ID from the canonical request (model + body).
2. Submit the step to `POST /v0/executions/{id}/steps`.
3. On `replay`, return the recorded response and skip the provider. On `proceed`,
   forward to the provider and record the response via `.../complete` (or
   `.../fail`).

Any interception point that speaks the step contract makes LLM calls durable,
whether that's an SDK-provided HTTP client or your own gateway. See the
[HTTP API](api.md#agent-api) for the exact request and response shapes.
