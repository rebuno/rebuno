# Clients

`Client` creates executions and inspects what they did. Your backend, a script,
or an operator uses it, not the agent handler.

```ts
import { Client } from "rebuno";

const client = new Client({
  baseUrl: "http://localhost:8080",  // or REBUNO_URL
  apiKey: "...",                     // or REBUNO_API_KEY
  timeout: 35000,                    // ms; default
});
```

`baseUrl` is required, from the option or `REBUNO_URL`. `apiKey` is optional and
sent as `Authorization: Bearer ...` when present.

## Executions

```ts
// create an execution (dispatches the agent)
const execution = await client.create(
  "dev-agent",
  { prompt: "hello" },         // optional; the object your handler receives
);

await client.get(execution.id);      // current state
await client.cancel(execution.id);   // request cancellation
```

`create` and `get` return an [`Execution`](#types). Poll `get` or read the event
log to watch it progress.

## Event log and steps

Read the raw event stream, or the steps it produced:

```ts
const events = await client.events(execution.id, { afterSeq: 0, limit: 100 });

const steps = await client.listSteps(execution.id, { status: "" });
const step = await client.getStep(execution.id, stepId);
```

`events` is paginated by `afterSeq`: pass the last `eventSeq` you've seen.
`limit` defaults to 100.

## Approvals

When policy requires approval for a step, the execution blocks and an approval
is created. Inspect and resolve them through `Client`:

```ts
const pending = await client.listApprovals({ status: "pending" });

await client.grantApproval(pending[0].id, { decidedBy: "alice", rationale: "looks fine" });
// or
await client.denyApproval(pending[0].id, { decidedBy: "alice", rationale: "not allowed" });

const one = await client.getApproval(approvalId);
```

Granting an approval lets the kernel re-dispatch. The handler replays its
recorded steps and proceeds past the one that was waiting. From the handler's
perspective the blocked call simply returns once approved.

## Errors

Failed requests throw typed errors: `NotFoundError`, `UnauthorizedError`,
`ForbiddenError`, `ValidationError`, `PolicyError`, `NetworkError`, and others,
all subclasses of `RebunoError`. See [Errors](errors.md).

## Types

`Client` returns plain objects with camelCase fields.

- `Execution`: `id`, `agentId`, `input`, `status`, `output`,
  `failureReason`. `status` is an `ExecutionStatus`, one of `pending`,
  `running`, `blocked`, `completed`, `failed`, `cancelled`.
- `Step`: `stepId`, `executionId`, `kind`, `target`, `argsHash`, `occurrence`,
  `status`, `idempotency`, `args`, `result`, `error`.
- `Event`: `executionId`, `eventSeq`, `type`, `payload`, `occurredAt`.
- `Approval`: `id`, `stepId`, `executionId`, `status`, `message`, `decidedBy`,
  `rationale`.

All are exported as types from `rebuno`.
