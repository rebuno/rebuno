# Policy

Match actual submitted tool names, kinds, and JSON arguments, including adapter
transformations and MCP prefixes. LLM targets are request-body model IDs.
Preserve existing permissions unless changing them is requested.

## Bundle

For a new restricted agent, deny by default and allow the intended effects.
Adapt this example to the user's tools:

```yaml
default_action: deny
rules:
  - id: allow-search
    when: { step_kind: tool_call, target: search }
    then: { decision: allow }
  - id: approve-email
    when: { step_kind: tool_call, target: send_email }
    then:
      decision: require_approval
      reason: sending email needs review
      approval_config: { timeout: 5m, message: review recipient and body }
```

For LLM access, add `step_kind: llm_call` with the actual model target(s),
or allow that kind generally if intended. The example above does not allow it.

## Rules that matter

- First matching rule wins; specific restrictions belong before broad allows.
  Each rule needs a unique `id`; decisions are `allow`, `deny`, `require_approval`.
- All supplied `when` fields must match. `target` / `targets` support Go
  `path.Match` globs; `agent_id` / `agent_ids` match agent names.
  `step_kind` is `tool_call`, `llm_call`, or `local`.
- `arguments` maps required argument fields to predicates: `equals`,
  `contains`, `one_of`, `regex` (RE2). Values compare as strings, and all
  constraints must pass. Example under `when`:
  `arguments: { environment: { one_of: [staging, preview] } }`.
  Unknown fields and empty predicates are rejected; do not invent nested-path
  or numeric comparison syntax.
- No bundle means unrestricted, including production. A bundle defaults to
  deny, but unmatched `local` steps remain allowed. Govern them explicitly
  with a matching target/kind rule. Unwrapped effects bypass policy entirely.
- Recorded outcomes replay without reevaluating a changed policy.
- Approval `approvers` checks the caller-supplied `decided_by` string; the shared
  bearer token has no individual identity. It is not authenticated user access
  control.

## Validate and apply

Use existing policy cases, or probe locally without creating test case files:

```bash
rebuno policy test policies/my-agent.yaml --target search --args '{"query":"hello"}'
```

For the sample, inspect the printed `allow` / `allow-search`. Also probe the
gated target, unknown targets, predicate mismatches, and rule overlap.
A probe's exit status alone does not assert the expected decision.
Use `--kind llm_call` or `--kind local` for those kinds. Probes do not run effects
or verify stateful limits and approval dispatch.

Attach the file through `policy_file` in the manifest, or apply an authorized
replacement with `rebuno policy set my-agent policies/my-agent.yaml`.
Check the selected kernel and agent first; drafting alone does not authorize
replacing a live bundle.

For approval integration, gate a harmless local tool after an allowed tool.
Confirm blocking before the gated body runs, inspect `rebuno approval ls`,
and grant that local approval with `rebuno approval grant <approval-id>`.
Confirm the same execution completes and earlier tool-body logs appear once.
Keep the in-memory dev kernel running.

For rate limits, budgets, advanced predicates, and historical policy comparison,
read [policy](https://github.com/rebuno/rebuno/blob/main/docs/policy.md) and
[CLI](https://github.com/rebuno/rebuno/blob/main/docs/cli.md) only when needed.
