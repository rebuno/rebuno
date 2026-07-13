# Clients

`rebuno.Client` is the other side of an [`Agent`](agents.md): you use it to
create executions and inspect what they did. It talks to the kernel's
client/admin routes with Bearer auth. This is what your backend, a script, or an
operator uses — not the agent handler.

```python
from rebuno import Client

client = Client(
    base_url="http://localhost:8080",  # or REBUNO_URL
    api_key="...",                     # or REBUNO_API_KEY
    timeout=35.0,                      # default
)
```

`base_url` is required (from the argument or `REBUNO_URL`). `api_key` is optional
but sent as `Authorization: Bearer ...` when present.

`Client` is an async context manager, so it closes its connection pool for you:

```python
async with Client() as client:
    execution = await client.create("dev-agent", input={"prompt": "hello"})
    ...
```

Otherwise call `await client.close()` when you're done.

## Executions

```python
# create an execution (dispatches the agent)
execution = await client.create(
    "dev-agent",
    input={"prompt": "hello"},   # optional; shape matches the handler signature
    agent_version="",            # optional; pin a specific agent version
)

execution = await client.get(execution.id)   # current state
await client.cancel(execution.id)             # request cancellation
```

`create` and `get` return an [`Execution`](#models); poll `get` (or read the
event log) to watch it progress.

## Event log and steps

Every execution is event-sourced. You can read the raw event stream or the steps
it produced:

```python
events = await client.events(execution.id, after_seq=0, limit=100)

steps = await client.list_steps(execution.id, status="")   # status filter optional
step = await client.get_step(execution.id, step_id)
```

`events` is paginated by `after_seq` (pass the last `event_seq` you've seen);
`limit` defaults to 100.

## Approvals (human-in-the-loop)

When policy requires approval for a step, the execution blocks and an approval is
created. Approvals are inspected and resolved through `Client`:

```python
pending = await client.list_approvals(status="pending")   # default status

await client.grant_approval(pending[0].id, decided_by="alice", rationale="looks fine")
# or
await client.deny_approval(pending[0].id, decided_by="alice", rationale="not allowed")

one = await client.get_approval(approval_id)
```

Granting an approval lets the kernel re-dispatch the execution; the handler
replays its recorded steps and proceeds past the one that was waiting. Your
handler code doesn't change — from its perspective the blocked tool call simply
returns once approved.

## Errors

Failed requests raise typed exceptions — `NotFoundError`, `UnauthorizedError`,
`ValidationError`, `PolicyError`, `NetworkError`, and so on, all subclasses of
`rebuno.RebunoError`. See [Errors](errors.md).

## Models

`Client` returns pydantic models (they ignore unknown fields, so kernel
additions won't break you):

- **`Execution`** — `id`, `agent_id`, `agent_version`, `input`, `status`,
  `output`, `failure_reason`. `status` is an `ExecutionStatus`
  (`pending`/`running`/`blocked`/`completed`/`failed`/`cancelled`).
- **`Step`** — `step_id`, `execution_id`, `kind`, `target`, `status`,
  `idempotency`, `args`, `result`, `error`.
- **`Event`** — `execution_id`, `event_seq`, `type`, `payload`, `occurred_at`.
- **`Approval`** — `id`, `step_id`, `execution_id`, `status`, `message`,
  `decided_by`, `rationale`.

These live in `rebuno.types`.
