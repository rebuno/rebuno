# Clients

`Client` is the other side of an [`Agent`](agents.md): you use it to create
executions and inspect what they did. It talks to the kernel's client/admin
routes with Bearer auth. This is what your backend, a script, or an operator uses
— not the agent handler.

```ts
import { Client } from "rebuno";

const client = new Client({
  baseUrl: "http://localhost:8080",  // or REBUNO_URL
  apiKey: "...",                     // or REBUNO_API_KEY
  timeout: 35000,                    // ms; default
});
```

`baseUrl` is required (from the option or `REBUNO_URL`). `apiKey` is optional but
sent as `Authorization: Bearer ...` when present. The client is `fetch`-based and
holds no connection pool, so there's nothing to close — construct one and use it.

## Executions

```ts
// create an execution (dispatches the agent)
let execution = await client.create(
  "dev-agent",
  { prompt: "hello" },              // optional input; shape matches the handler
  { agentVersion: "" },             // optional; pin a specific agent version
);

execution = await client.get(execution.id);   // current state
await client.cancel(execution.id);             // request cancellation
```

`create` and `get` return an [`Execution`](#models); poll `get` (or read the
event log) to watch it progress.

## Event log and steps

Every execution is event-sourced. You can read the raw event stream or the steps
it produced:

```ts
const events = await client.events(execution.id, { afterSeq: 0, limit: 100 });

const steps = await client.listSteps(execution.id, { status: "pending" });   // status filter optional
const step = await client.getStep(execution.id, stepId);
```

`events` is paginated by `afterSeq` (pass the last `eventSeq` you've seen);
`limit` defaults to 100. Each [`Event`](#models) carries a `type` (e.g.
`step.proposed`, `step.awaiting_approval`, `execution.completed`) and a `payload`.

## Approvals (human-in-the-loop)

When policy requires approval for a step, the execution blocks and an approval is
created. Approvals are inspected and resolved through `Client`:

```ts
const pending = await client.listApprovals({ status: "pending" });   // default status

await client.grantApproval(pending[0].id, { decidedBy: "alice", rationale: "looks fine" });
// or
await client.denyApproval(pending[0].id, { decidedBy: "alice", rationale: "not allowed" });

const one = await client.getApproval(approvalId);
```

Granting an approval lets the kernel re-dispatch the execution; the handler
replays its recorded steps and proceeds past the one that was waiting. Your
handler code doesn't change — from its perspective the blocked tool call simply
returns once approved.

## Errors

Failed requests throw typed errors — `NotFoundError`, `UnauthorizedError`,
`ValidationError`, `PolicyError`, `NetworkError`, and so on, all subclasses of
`RebunoError`. See [Errors](errors.md).

## Models

`Client` returns plain objects parsed from the kernel's JSON (snake_case wire
fields are exposed as camelCase; unknown fields are ignored, so kernel additions
won't break you). The types live in the package's exports:

- **`Execution`** — `id`, `agentId`, `agentVersion`, `input`, `status`, `output`,
  `failureReason`. `status` is an `ExecutionStatus`
  (`pending`/`running`/`blocked`/`completed`/`failed`/`cancelled`).
- **`Event`** — `executionId`, `eventSeq`, `type`, `payload`, `occurredAt`.
- **`Approval`** — `id`, `stepId`, `executionId`, `status`, `message`,
  `decidedBy`, `rationale`.
- **`Step`** — `stepId`, `executionId`, `kind`, `target`, `status`,
  `idempotency`, `args`, `result`, `error`.
