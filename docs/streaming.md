# Live streaming

The event ledger (`GET /v0/executions/{id}/events`) is completion-granular: a
polling client sees an `llm_call` step's whole output only when its
`step.succeeded` event lands. That is the right behaviour for durability and
replay — the ledger records completed facts, and replaying a finished step has
nothing to stream.

Streaming only has meaning **live**, while a step is executing. For that,
Rebuno offers an ephemeral side channel that is entirely separate from the
ledger. Deltas on it are best-effort and never persisted; `step.succeeded`
remains the single source of truth.

## Endpoints

**Producer (agent → kernel), HMAC auth:**

    POST /v0/executions/{id}/steps/{step_id}/stream
    body: {"seq": <int64>, "data": "<opaque provider chunk text>"}

The agent, while teeing the provider's stream during `proceed`, batches output
(~50ms) and POSTs each batch. `data` is opaque — the kernel never parses it.
`seq` is a per-step counter the agent assigns. A batch's `data` must stay under
7000 bytes (Postgres NOTIFY payload limit); keep batches small.

**Consumer (client → kernel), Bearer auth:**

    GET /v0/executions/{id}/stream        (Server-Sent Events)
    frames: data: {"step_id":"...","seq":3,"data":"..."}\n\n
            : keep-alive\n\n   (every 15s)

## How it fans out

The producing replica republishes each delta on one Postgres `LISTEN/NOTIFY`
channel (`rebuno_stream`); every replica runs a single listener that delivers
to its locally-connected SSE subscribers. No new tables, no migration.

## Client contract

- Treat the SSE stream as a live **tail** and `/events` as **truth**. On the
  terminal `step.succeeded`, read the whole result from `/events`.
- Use `seq` to detect a gap (a dropped delta or a slow consumer). On a gap,
  stop rendering deltas and fall back to `/events`.
- A client connecting mid-stream sees only deltas from connect-time onward
  (there is no replay buffer). Earlier output comes from `/events` on
  completion.

