# Errors

Every SDK error subclasses `RebunoError`, so you can catch that one type as a
backstop. `RebunoError` carries an optional `.details` object.

```ts
import { RebunoError, PolicyError } from "rebuno";

try {
  await client.create("dev-agent", { /* ... */ });
} catch (e) {
  if (e instanceof PolicyError) {
    // denied by policy
  } else if (e instanceof RebunoError) {
    // anything else from the SDK
  } else {
    throw e;
  }
}
```

## Hierarchy

```
RebunoError                  .details
├─ NetworkError              connection refused, timeout — no HTTP response
├─ APIError                 the kernel returned an error envelope {code, message}
│  │                        (.code, .statusCode)
│  ├─ ValidationError       400  request validation failed
│  ├─ UnauthorizedError     401  authentication failed
│  ├─ NotFoundError         404  resource not found
│  ├─ ConflictError         409  conflict
│  ├─ PolicyError           403  denied by policy (carries .ruleId)
│  └─ StepIDMismatch        409  kernel rejected the SDK-computed step id
├─ ToolError                a tool's effect body failed (carries .toolId, .stepId, .retryable)
├─ RateLimited              a step was rejected by a policy rate limit (carries .reason)
├─ Blocked                  internal: a step is awaiting approval (carries .approvalId)
└─ Terminated               internal: the execution is terminal (e.g. cancelled)
```

### `APIError` and its subclasses

Thrown when the kernel returns a `>= 400` response. Carry `.code` (the kernel
error code), `.statusCode` (HTTP status), and `.details`. The SDK maps error
codes to these subclasses so `Client` and the agent's kernel client can't
disagree on which class a code means.

- **`PolicyError`** — an action was denied by policy. Also has `.ruleId` naming
  the rule that denied it.
- **`StepIDMismatch`** — the kernel rejected the step id the SDK computed
  (`409 step_id_divergence`). This signals the agent's effect sequence diverged
  from a prior dispatch — usually **non-determinism that wasn't wrapped in
  [`step()`](steps.md)**. See
  [determinism and step ids](internals.md#determinism-and-step-ids).

### `ToolError`

Thrown when a tool's effect body throws. Carries `.toolId`, `.stepId`, and a
`.retryable` flag. When thrown inside a handler, the agent fails the execution.

### `RateLimited`

A step was rejected because a policy rate limit was exceeded. Has `.reason`.

### `Blocked` and `Terminated` — internal control flow

You normally won't see these. They're control-flow signals the SDK throws to
unwind a dispatch cleanly:

- **`Blocked`** — a tool call hit a step that's awaiting human approval. The
  agent's webhook handler catches it and returns `200`; the execution is already
  `blocked` in the kernel and will be re-dispatched when the approval is
  resolved. Carries `.approvalId`.
- **`Terminated`** — the execution became terminal (e.g. cancelled) mid-dispatch.
  The dispatch unwinds and returns `200`.

Both are caught by the agent runtime; don't catch them in your handler unless you
have a specific reason (and re-throw if you do).

## What's exported

`RebunoError`, `NetworkError`, `APIError`, `ValidationError`, `UnauthorizedError`,
`NotFoundError`, `ConflictError`, `PolicyError`, `StepIDMismatch`, `ToolError`,
`RateLimited`, `Blocked`, and `Terminated` are all importable from `rebuno`.
