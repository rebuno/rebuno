---
name: rebuno
description: Set up Python or TypeScript agents with Rebuno, integrate the SDK into existing applications, and write or troubleshoot policies and approvals.
---

# Rebuno

Work in the user's application. Read only the relevant references:

- **New agent:** [setup](references/setup.md) and [Python](references/python.md)
  or [TypeScript](references/typescript.md).
- **Existing agent:** the selected language guide; setup only for registration
  or hosting changes.
- **Permissions:** [policy](references/policy.md), using actual tool definitions
  or recorded targets and arguments.

Preserve the framework, provider configuration, and requested permissions.
Check installed SDK/provider APIs; linked docs track main.

## How calls become steps

`Agent` establishes the execution context. These boundaries submit effects to
the kernel before running them:

| Kind | Python | TypeScript |
|------|--------|------------|
| Tool | `@tool` / `wrap_tool` | `defineTool` / `wrapTool` |
| LLM HTTP request | `http_client()` / `RebunoTransport` | `rebunoFetch` / `createRebunoFetch` |
| Local value | `step(name, fn, ...)` | `step(name, fn, ...)` |

The kernel returns a recorded outcome or decides whether new work may proceed.
Unwrapped effects bypass recording and policy. Tool wrappers alone do not
intercept the model client.

On resume the handler starts from the top, reusing recorded tool results and
LLM responses. Step identity depends on execution, kind, target, arguments, and
occurrence count for identical calls. Preserve those calls from the original
input and recorded results; distinct effects may run in parallel. Generate time,
random values, or IDs **inside** a `step()` callback before using them in later
arguments. Record mutable external reads too when they affect subsequent calls.

## Integrate and verify

Find the handler, model client, actual tool callbacks, and existing registration.
Bind the handler, wrap individual effects, and configure the provider transport.
Retain raw functions for non-Rebuno callers. Check whether framework checkpoints
or caches skip recorded calls on resume; keep recording at individual effect
boundaries.

Default `safe_to_retry` may repeat an effect that started without recording a
result. For non-idempotent writes, `at_most_once` reports `indeterminate` instead;
reconcile external state before retrying. Keep external idempotency keys stable.

Create an execution through the kernel; inspect output and tool/`llm_call`
steps. Run relevant checks/probes; report startup commands and unverified behavior.
Shared configuration changes and real approvals require the user's authorization.
