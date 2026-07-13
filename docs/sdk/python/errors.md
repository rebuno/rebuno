# Errors

Every SDK exception subclasses `rebuno.RebunoError`, so you can catch that one
type as a backstop. `RebunoError` carries an optional `.details` dict.

```python
from rebuno import RebunoError, PolicyError, NotFoundError

try:
    await client.create("dev-agent", input={...})
except PolicyError as e:
    ...            # denied by policy
except RebunoError as e:
    ...            # anything else from the SDK
```

## Hierarchy

```
RebunoError
├─ NetworkError            connection refused, timeout — no HTTP response
├─ APIError               the kernel returned an error envelope {code, message}
│  ├─ ValidationError     400  request validation failed
│  ├─ UnauthorizedError   401  authentication failed
│  ├─ NotFoundError       404  resource not found
│  ├─ PolicyError         403  denied by policy (carries .rule_id)
│  └─ StepIDMismatch      409  kernel rejected the SDK-computed step id
├─ ToolError              a tool's effect body failed (carries .tool_id, .step_id)
├─ RateLimited            a step was rejected by a policy rate limit
├─ Blocked                internal: a step is awaiting approval
└─ Terminated             internal: the execution is terminal (e.g. cancelled)
```

### `APIError` and its subclasses

Raised when the kernel returns a `>= 400` response. Carry `.code` (the kernel
error code), `.status_code` (HTTP status), and `.details`. The SDK maps error
codes to these subclasses so `Client` and the agent's kernel client can't
disagree on which exception a code means.

- **`PolicyError`** — an action was denied by policy. Also has `.rule_id` naming
  the rule that denied it.
- **`StepIDMismatch`** — the kernel rejected the step id the SDK computed
  (`409 step_id_divergence`). This signals the agent's effect sequence diverged
  from a prior dispatch — usually **non-determinism that wasn't wrapped in
  [`rebuno.step()`](steps.md)**. See
  [determinism and step ids](internals.md#determinism-and-step-ids).

### `ToolError`

Raised when a tool's effect body throws. Carries `.tool_id`, `.step_id`, and a
`.retryable` flag. When raised inside a handler, the agent fails the execution.

### `RateLimited`

A step was rejected because a policy rate limit was exceeded. Has `.reason`.

### `Blocked` and `Terminated` — internal control flow

You normally won't see these. They're control-flow signals the SDK raises to
unwind a dispatch cleanly:

- **`Blocked`** — a tool call hit a step that's awaiting human approval. The
  agent's webhook handler catches it and returns `200`; the execution is already
  `blocked` in the kernel and will be re-dispatched when the approval is
  resolved. Carries `.approval_id`.
- **`Terminated`** — the execution became terminal (e.g. cancelled) mid-dispatch.
  The dispatch unwinds and returns `200`.

Both are caught by the agent runtime; don't catch them in your handler unless you
have a specific reason (and re-raise if you do).

## What's exported

`RebunoError`, `APIError`, `ValidationError`, `UnauthorizedError`,
`NotFoundError`, `PolicyError`, `StepIDMismatch`, `ToolError`, `RateLimited`,
`Blocked`, `Terminated`, and `NetworkError` are all importable from `rebuno`.
