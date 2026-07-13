# Steps

`rebuno.step()` records non-deterministic *local* computation so its result is
captured once and replays identically on resume. Use it for anything that
influences which effects run but isn't a tool or LLM call: the current time,
random choices, freshly generated ids.

```python
import random
from rebuno import step

chosen = await step("pick_winner", random.choice, args={"candidates": candidates})
now = await step("now", time.time)
```

Why this matters: on resume the handler re-runs from the top. If it calls
`random.choice(...)` or `time.time()` directly, it gets a *different* value the
second time, and the sequence of effects that follow can diverge from what was
recorded — which the kernel rejects (see
[`StepIDMismatch`](internals.md#determinism-and-step-ids)). Wrapping the
non-determinism in `step()` records the value the first time and replays that
same value on every subsequent dispatch, keeping the run deterministic.

## Signature

```python
async def step(
    name: str,
    fn: Callable[..., Any],
    args: dict[str, Any] | None = None,
    idempotency: str = "safe_to_retry",
) -> Any
```

- `name` — the step id it's recorded under.
- `fn` — the work to run. It's called as `fn(**args)`.
- `args` — the payload used for step identity/hashing, passed as keyword
  arguments to `fn`. Pass `None` (default) when `fn` takes no arguments.
- `idempotency` — mirrors [`@tool`](tools.md#idempotency): `safe_to_retry`
  (default) for reads/non-determinism; `at_most_once` for local side effects that
  must not re-run on resume.

The result must be JSON-serializable, since it's recorded.

Under the hood a step is recorded through the same path as a tool call (it's a
`tool_call`-kind step), so everything that applies to tools — replay, policy,
idempotency — applies to `step()` too. The distinction is intent: use `@tool` for
actions with external effects, `step()` for local non-determinism.

Calling `step()` outside an active execution raises `RuntimeError`.
