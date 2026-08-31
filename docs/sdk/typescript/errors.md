# Errors

Every SDK error subclasses `RebunoError`, so you can catch that one class as a
backstop.

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
RebunoError
├─ NetworkError            connection refused or timeout, no HTTP response
├─ APIError                the kernel returned an error envelope {code, message}
│  ├─ ValidationError      400  request validation failed
│  ├─ UnauthorizedError    401  authentication failed
│  ├─ ForbiddenError       403  the caller is not permitted
│  ├─ NotFoundError        404  resource not found
│  ├─ PolicyError          403  denied by policy (carries .ruleId)
│  └─ LeaseSuperseded      409  internal: a newer attempt owns this dispatch
├─ ToolError               a tool's effect body failed (carries .toolId, .stepId)
├─ RateLimited             a step was rejected by a policy rate limit
├─ Blocked                 internal: a step is awaiting approval
└─ Terminated              internal: the execution is terminal (e.g. cancelled)
```

### `APIError` and its subclasses

Thrown when the kernel returns a `>= 400` response. They carry `.code` (the
kernel's error code) and `.statusCode`. An unmapped code throws a plain
`APIError`.

`PolicyError` also carries `.ruleId`, naming the rule that denied the call.

### `ToolError`

Thrown when a tool's effect body throws, and when an `at_most_once` step comes
back `indeterminate`. Carries `.toolId` and `.stepId`, and fails the execution
when it reaches the handler boundary.

### `RateLimited`

A step was rejected because a policy rate limit was exceeded. Carries `.reason`.

### `Blocked`, `Terminated`, and `LeaseSuperseded`

You normally won't see these. They are control-flow signals the SDK throws to
unwind a dispatch cleanly.

- `Blocked` means a step is waiting on a human approval, either the one your
  code just submitted or an earlier one that already parked the execution. The
  kernel re-dispatches once the approval is resolved.
- `Terminated` means the execution went terminal partway through the dispatch,
  usually a cancel.
- `LeaseSuperseded` means a newer delivery attempt owns the dispatch. The kernel
  refuses every mutation from the attempt this handler was sent under, so it
  stops where it stands and leaves the execution to its replacement.

`Agent` catches all three. Don't catch them yourself without a reason, and
re-throw if you do. See [Dispatch and resume](agents.md#dispatch-and-resume)
for the backstops that cover a handler which swallows one.

## Helpers

- `raiseForRefusal(err)` re-throws a Rebuno refusal a provider client wrapped in
  its own error type. You get back `Blocked`, `PolicyError`, `RateLimited`,
  `Terminated`, or `LeaseSuperseded`. Any other error is left alone. See
  [LLM calls](llm-calls.md#refused-calls).
- `failureReason(err)` is the text the agent records in an execution's
  `failureReason`. Everything before the first colon is a stable token, either
  a kernel reason such as `policy_denied` or one of `tool_error`, `agent_error`,
  `input_invalid`.

## What's exported

`RebunoError`, `APIError`, `ValidationError`, `UnauthorizedError`,
`ForbiddenError`, `NotFoundError`, `ConflictError`, `PolicyError`, `ToolError`,
`RateLimited`, `Blocked`, `Terminated`, `LeaseSuperseded`, `NetworkError`,
`raiseForRefusal`, and `failureReason` are all importable from `rebuno`.
