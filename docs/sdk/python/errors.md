# Errors

Every SDK exception subclasses `rebuno.RebunoError`, so you can catch that one
type as a backstop.

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
├─ NetworkError            connection refused or timeout, no HTTP response
├─ APIError                the kernel returned an error envelope {code, message}
│  ├─ ValidationError      400  request validation failed
│  ├─ UnauthorizedError    401  authentication failed
│  ├─ ForbiddenError       403  the caller is not permitted
│  ├─ NotFoundError        404  resource not found
│  └─ PolicyError          403  denied by policy (carries .rule_id)
├─ ToolError               a tool's effect body failed (carries .tool_id, .step_id)
├─ RateLimited             a step was rejected by a policy rate limit
├─ Blocked                 internal: a step is awaiting approval
└─ Terminated              internal: the execution is terminal (e.g. cancelled)
```

### `APIError` and its subclasses

Raised when the kernel returns a `>= 400` response. They carry `.code` (the
kernel's error code) and `.status_code`. An unmapped code raises a plain
`APIError`.

`PolicyError` also carries `.rule_id`, naming the rule that denied the call.

### `ToolError`

Raised when a tool's effect body throws, and when an `at_most_once` step comes
back `indeterminate`. Carries `.tool_id` and `.step_id`, and fails the execution
when it reaches the handler boundary.

### `RateLimited`

A step was rejected because a policy rate limit was exceeded. Carries `.reason`.

### `Blocked` and `Terminated`

You normally won't see these. They are control-flow signals the SDK raises to
unwind a dispatch cleanly.

- `Blocked` means a step is waiting on a human approval, either the one your
  code just submitted or an earlier one that already parked the execution. The
  kernel re-dispatches once the approval is resolved.
- `Terminated` means the execution went terminal partway through the dispatch,
  usually a cancel.

`Agent` catches both. Don't catch them yourself without a reason, and re-raise
if you do. See [Dispatch and resume](agents.md#dispatch-and-resume) for the
backstops that cover a handler which swallows one.

## Helpers

- `raise_for_refusal(exc)` re-raises a Rebuno refusal a provider SDK wrapped in
  its own error type. You get back `Blocked`, `PolicyError`, `RateLimited`, or
  `Terminated`. Any other exception is left alone. See
  [LLM calls](llm-calls.md#refused-calls).
- `failure_reason(exc)` is the text the agent records in an execution's
  `failure_reason`. Everything before the first colon is a stable token, either
  a kernel reason such as `policy_denied` or one of `tool_error`, `agent_error`,
  `input_invalid`.

## What's exported

`RebunoError`, `APIError`, `ValidationError`, `UnauthorizedError`,
`ForbiddenError`, `NotFoundError`, `PolicyError`, `ToolError`, `RateLimited`,
`Blocked`, `Terminated`, `NetworkError`, `raise_for_refusal`, and
`failure_reason` are all importable from `rebuno`.
