# Agents

An agent is a stateless HTTP service. The kernel POSTs to its 
webhook when there is work to do, and the agent sends its effects
(tool and LLM calls) back through the kernel's API.

## Register an agent

An agent needs a `webhook_url` and an HMAC `secret`. There are three ways to
register one: a provisioning manifest at kernel boot (`--config`), 
`rebuno agent add`, or the admin API (`POST /v0/agents`). See
[Provisioning agents](deployment.md#provisioning-agents).

```yaml
agents:
  - id: researcher
    webhook_url: http://localhost:5001/webhook
    secret: researcher-secret
    policy_file: policies/research.yaml
```

## Dispatch lifecycle

The kernel POSTs to the webhook every time it has work for an execution. The
agent runs the same sequence on the first dispatch and on every resume:

1. **Dispatch.** The webhook arrives with `{execution_id, dispatch_id,
   dispatch_attempt}` and a signature header,
   `Rebuno-Signature: sha256=<HMAC-SHA256(secret, body)>`. The agent checks the
   signature and acks with `200 OK` right away. The kernel delivers at least
   once, so the same dispatch can arrive twice. Key the handler on
   `(execution_id, dispatch_id, dispatch_attempt)`.
2. **Fetch input.** The agent reads the execution's original input with
   `GET /v0/executions/{id}`.
3. **Run.** The agent runs its own logic from the top, with that input.
4. **Submit each effect.** Before every tool or LLM call, the agent submits a step
   (`POST /v0/executions/{id}/steps`, carrying the dispatch id and attempt). The
   kernel answers with a decision:
   - `replay` → the recorded result comes back. The effect does not run.
   - `proceed` → the agent runs the effect, then reports how it went
     (`.../complete` or `.../fail`).
   - `denied` → policy rejected the call. The agent surfaces it as an error.
   - `rate_limited` → a rule's rate limit refused the call. The agent surfaces it
     as an error.
   - `blocked` → the agent stops and exits the dispatch.
   - `execution_blocked` → an earlier step is awaiting approval, so no new effect
     can start. The agent stops and exits, same as `blocked`.
   - `execution_terminal` → the execution is already cancelled or finished. The
     agent exits cleanly.
5. **Block.** `blocked` has two causes. A human approval is pending, and the
   response carries an `approval_id` while the execution moves to `blocked`. Or a
   rate limit parked the step, and the execution stays `running`. Either way the
   agent holds nothing in memory, so the process can exit here, or crash, without
   losing anything.
6. **Resume.** The kernel dispatches again once the approval resolves or the rate
   limit's wait is up. The agent runs from the top, every effect it already did
   comes back as `replay`, and the step that blocked gets a fresh decision.
7. **Complete.** When the agent's logic finishes, it reports the result with
   `POST /v0/executions/{id}/complete`, and the kernel records
   `execution.completed`.

See the [HTTP API](api.md) for the exact request and response shapes, and
[Architecture](architecture.md) for how step identity makes replay work.

Tool calls are written into the agent's own code, so submitting a step for one is
easy. LLM calls are HTTP requests buried inside a provider SDK, so something has
to intercept them at the HTTP layer before step 4 can record them.
[LLM calls](llm-calls.md) explains how that works, and also how to wire it up to your
own gateway.

## What an agent must guarantee

Replay only lines up if the agent makes the same calls in the same order when it
sees the same input and the same earlier results.

- The agent can pick its calls and their order from the input and from earlier
  results. It must not branch on the clock, a random number, or anything else
  local to the process.
- If something non-deterministic decides which effects fire, record it as a
  `local` step. It lands in the log and replays to the same value. Both SDKs
  expose this as `step(name, fn)`. See [Local steps](tools.md#local-steps).

Replay only covers steps recorded under the execution being dispatched. The agent
loads any other context from its own store.

## Idempotency and at-least-once delivery

Webhooks are delivered at least once, so the same dispatch can arrive twice. A
redelivery re-submits the same step IDs and short-circuits on the recorded
results.

A dispatch that goes quiet is reclaimed and delivered again under the next
`dispatch_attempt`. Calls from the earlier attempt are refused with
`409 lease_superseded`, and the agent stops without failing the execution.

A crash can still orphan an effect, where a step started but never recorded a
result. What the kernel does then depends on the idempotency mode the step
declared. See [Idempotency modes](tools.md#idempotency-modes).
