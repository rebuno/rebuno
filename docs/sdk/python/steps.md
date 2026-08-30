# Steps

`rebuno.step()` records local non-determinism once so it replays identically on
resume. Use it for anything that decides which effects run next but isn't a tool
or LLM call, like the current time, a random choice, or a fresh id.

```python
import random
import time
from rebuno import step

now = await step("now", time.time)
chosen = await step("pick_winner", random.choice, args={"seq": candidates})
```

On resume the handler re-runs from the top. Calling `time.time()` or
`random.choice(...)` directly gives a different value the second time, so the
effects that follow carry different arguments. Different arguments mean
different step ids, and the kernel runs those effects again for real instead of
replaying them. See [determinism and step ids](internals.md#determinism-and-step-ids).

## Signature

```python
async def step(
    name: str,
    fn: Callable[..., Any],
    args: dict[str, Any] | None = None,
    idempotency: str = "safe_to_retry",
) -> Any
```

- `name` is the step's `target`, the same field a policy rule matches on. The
  step id itself is derived by the kernel.
- `fn` is the work to run. It's called as `fn(**args)`.
- `args` is the payload used for step identity, passed as keyword arguments to
  `fn`. Pass `None` (the default) when `fn` takes no arguments.
- `idempotency` mirrors [`@tool`](tools.md#idempotency): `safe_to_retry`
  (default) for reads and non-determinism, `at_most_once` for local side effects
  that must not re-run on resume.

The result has to be JSON-serializable, since it's recorded.

## What it records

A `step()` call is recorded with kind `local` and travels the same submission
path as a tool call. Replay, policy, and idempotency all work the same way.

Policy treats `local` as a separate kind. A `local` step that matches no rule is
allowed whatever `default_action` says, so a deny-by-default bundle won't block
one.

Calling `step()` outside an active execution raises `RuntimeError`.
