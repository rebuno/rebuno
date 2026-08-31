# Live streaming

The event log (`GET /v0/executions/{id}/events`) carries no step output. A
polling client learns an `llm_call` finished when `step.succeeded` lands, then
reads the result from `GET /v0/executions/{id}/steps/{step_id}`.

To watch a step while it runs, Rebuno publishes deltas on a side channel that is
never persisted.

## Endpoints

Producer (agent to kernel), HMAC auth:

```http
POST /v0/executions/{id}/steps/{step_id}/stream
{"seq": <int64>, "data": "<opaque provider chunk text>"}
```

The agent tees the provider's stream while it handles a `proceed` and POSTs each
batch. `data` is opaque and the kernel never parses it. `seq` is a per-step
counter the agent assigns, starting at 0. A `data` field over 7000 bytes is
rejected with `400` and nothing is truncated for you, so the agent has to do the
slicing. The cap leaves headroom under Postgres's 8000-byte NOTIFY limit for the
envelope the kernel wraps around the delta.

The kernel returns `204` whether or not the delta reached anyone, so an agent
cannot tell from the response that a client is listening.

Consumer (client to kernel), Bearer auth:

```
GET /v0/executions/{id}/stream        (Server-Sent Events)

frames: data: {"step_id":"...","seq":3,"data":"..."}\n\n
        : keep-alive\n\n   (every 15s)
```

### SDK batching behavior

The Python and TypeScript SDKs flush after 2000 characters or 50ms, whichever
comes first. Each flush is sliced into 1750-character deltas so a run of 4-byte
UTF-8 characters stays under the kernel's 7000-byte cap. Every delta gets its own
`seq`.

A replayed step publishes nothing. The SDK streams the recorded body to the
caller without touching the side channel.

## Fan-out across replicas

Under `rebuno server`, the replica that took the delta republishes it on one
Postgres `LISTEN/NOTIFY` channel (`rebuno_stream`). Every replica runs a single
listener and delivers to the subscribers connected to it. `rebuno dev` uses an
in-process bus, since there is nothing to fan out to.

Each subscriber gets a 64-delta buffer, and the hub sends without blocking. A
consumer that falls behind drops deltas instead of stalling the producer.
`pg_notify` does not buffer either, so a delta published while no replica is
listening is gone.

## Client contract

- Treat the SSE stream as a live tail and the recorded step result as truth.
  Read it from `GET /v0/executions/{id}/steps/{step_id}` once `step.succeeded`
  lands.
- One execution stream carries every step's deltas, so track `seq` per
  `step_id`. A gap means a delta was dropped. Stop rendering and wait for the
  recorded result.
- A client connecting mid-stream sees only deltas from connect time onward.
  There is no replay buffer.
