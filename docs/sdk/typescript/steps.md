# Steps

`step()` records local non-determinism once so it replays identically on resume.
Use it for anything that decides which effects run next but isn't a tool or LLM
call, like the current time, a random choice, or a fresh id.

```ts
import { step } from "rebuno";

const now = await step("now", () => Date.now());
const chosen = await step("pick_winner", ({ candidates }) => candidates[0], { candidates });
```

On resume the handler re-runs from the top. Calling `Date.now()` or
`Math.random()` directly gives a different value the second time, so the effects
that follow carry different arguments. Different arguments mean different step
ids, and the kernel runs those effects again for real instead of replaying them.
See [determinism and step ids](internals.md#determinism-and-step-ids).

## Signature

```ts
function step<T>(
  name: string,
  fn: (args: Record<string, unknown>) => T | Promise<T>,
  args?: Record<string, unknown>,                   // default {}
  idempotency?: "safe_to_retry" | "at_most_once",   // default "safe_to_retry"
): Promise<T>
```

- `name` is the step's `target`, the same field a policy rule matches on. The
  step id itself is derived by the kernel.
- `fn` is the work to run. It's called as `fn(args)`, with the whole `args`
  object as the single argument.
- `args` is the payload used for step identity, and is passed to `fn`. Omit it
  (default `{}`) when `fn` takes no arguments.
- `idempotency` mirrors [`defineTool`](tools.md#idempotency): `safe_to_retry`
  (default) for reads and non-determinism, `at_most_once` for local side effects
  that must not re-run on resume.

The result has to be JSON-serializable, since it's recorded.

## What it records

A `step()` call is recorded with kind `local` and travels the same submission
path as a tool call. Replay, policy, and idempotency all work the same way.

Policy treats `local` as a separate kind. A `local` step that matches no rule is
allowed whatever `default_action` says, so a deny-by-default bundle won't block
one.

Calling `step()` outside an active execution throws an `Error`.
