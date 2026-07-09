# Agents

An agent is a **stateless HTTP service** registered with the kernel by name. It
exposes a webhook the kernel POSTs to, and it drives its effects (tool and LLM
calls) back through the kernel's API.

## Registering an agent

An agent needs a `webhook_url` and an HMAC `secret`. Register it via a
provisioning manifest at kernel boot (`--config`), the REPL (`agent add`), or the
admin API (`POST /v0/agents`). See [deployment.md](deployment.md#provisioning-agents).

```yaml
agents:
  - id: researcher
    webhook_url: http://localhost:5001/webhook
    secret: researcher-secret
    policy_file: policies/research.yaml
```

## The dispatch lifecycle

Each time the kernel has work for an execution it POSTs the agent's webhook. The
agent performs the same sequence on the first dispatch and on every resume — this
is what makes crashes and approvals transparent:

1. **Dispatch.** The webhook arrives with `{execution_id, dispatch_id}`, signed
   `Rebuno-Signature: sha256=<HMAC-SHA256(secret, body)>`. The agent verifies the
   signature and acks with `200 OK` immediately. (Delivery is at-least-once, so the
   handler must be safe under duplicate dispatch — key on `(execution_id, dispatch_id)`.)
2. **Hydrate.** The agent fetches the execution's input
   (`GET /v0/executions/{id}`) and its recorded steps
   (`GET /v0/executions/{id}/steps?status=terminal`) so it can replay prior effects.
3. **Run.** The agent runs its own logic from the top with the original input.
4. **Submit each effect.** For every tool or LLM call, the agent submits a step
   (`POST /v0/executions/{id}/steps`) and acts on the decision:
   - `replay` → the recorded result is returned; **the effect does not run**.
   - `proceed` → the agent performs the effect, then reports the outcome
     (`.../complete` or `.../fail`).
   - `denied` → policy rejected it; surface it as an error.
   - `blocked` → a human approval is required (see below).
   - `execution_terminal` → the execution is cancelled/done; exit cleanly.
5. **Block.** On a `blocked` decision the agent stops and exits the dispatch
   cleanly. The execution is `blocked` waiting on an approval. **The process may
   die here** — no state is held in memory.
6. **Resume.** When the approval resolves, the kernel re-dispatches. The agent runs
   from the top again; every prior effect returns `replay`, and the
   previously-blocked step now proceeds.
7. **Complete.** When the agent's logic finishes, it reports the result
   (`POST /v0/executions/{id}/complete`), and the kernel records
   `execution.completed`.

See the [HTTP API](api.md) for the exact request and response shapes, and
[architecture.md](architecture.md) for how step identity makes replay work.

Tool calls are explicit in agent code, but LLM calls are HTTP requests buried
inside a provider SDK, so they must be **intercepted** at the HTTP layer before
step 4 can record them. That mechanism — and how to implement it against your own
LLM gateway — has its own page: [LLM calls](llm-calls.md).

## What an agent must guarantee

Replay only short-circuits correctly if the agent reaches the **same sequence of
effects** given the same input and the same prior results:

- The order of tool and LLM calls must be a function of the input and prior effect
  results — not of wall-clock time, random numbers, or other local non-determinism.
- Non-deterministic work that changes *which* effects fire must itself be expressed
  as a recorded effect (a tool call), so it replays to the same value.

Rebuno records **effects, not conversations**. It does not reconstruct framework or
conversation state — the agent reloads whatever context it needs from its own store
at the start of each dispatch.

## Idempotency and at-least-once delivery

Webhook delivery is at-least-once, and two kernel replicas can race the same
execution. This is safe: every effect the agent re-issues short-circuits on its
step ID, so duplicate or concurrent dispatches converge rather than double-run.

When a crash orphans an effect (a step started but never recorded a result), how
the kernel recovers depends on the step's declared idempotency mode. See
[tools.md](tools.md#idempotency-modes).
