# Steps

`step()` records non-deterministic *local* computation so its result is captured
once and replays identically on resume. Use it for anything that influences which
effects run but isn't a tool or LLM call: the current time, random choices,
freshly generated ids.

```ts
import { step } from "rebuno";

const chosen = await step("pick_winner", ({ candidates }) => candidates[0], { candidates });
const now = await step("now", () => Date.now());
```

Why this matters: on resume the handler re-runs from the top. If it calls
`Math.random()` or `Date.now()` directly, it gets a *different* value the second
time, and the sequence of effects that follow can diverge from what was recorded
— which the kernel rejects (see
[`StepIDMismatch`](internals.md#determinism-and-step-ids)). Wrapping the
non-determinism in `step()` records the value the first time and replays that same
value on every subsequent dispatch, keeping the run deterministic.

## Signature

```ts
function step<T>(
  name: string,
  fn: (args: Record<string, unknown>) => T | Promise<T>,
  args?: Record<string, unknown>,          // default {}
  idempotency?: "safe_to_retry" | "at_most_once",  // default "safe_to_retry"
): Promise<T>
```

- `name` — the step id it's recorded under.
- `fn` — the work to run. It's called as `fn(args)` — the whole `args` object is
  passed as the single argument.
- `args` — the payload used for step identity/hashing, and passed to `fn`. Omit
  (default `{}`) when `fn` takes no arguments.
- `idempotency` — mirrors [`defineTool`](tools.md#idempotency): `safe_to_retry`
  (default) for reads/non-determinism; `at_most_once` for local side effects that
  must not re-run on resume.

The result must be JSON-serializable, since it's recorded.

Under the hood a step is recorded through the same path as a tool call (it's a
`tool_call`-kind step), so everything that applies to tools — replay, policy,
idempotency — applies to `step()` too. The distinction is intent: use
`defineTool` for actions with external effects, `step()` for local
non-determinism.

Calling `step()` outside an active execution throws an `Error`.
