# Policy

Policy governs which effects an agent may perform. A rule bundle is evaluated
**once per step submission** — for both tool calls and LLM calls — and returns
one of three decisions: `allow`, `deny`, or `require_approval`. Policy is
gate-keeping only; it never rewrites a request.

Each agent has its own bundle. Provide it inline (`policy:`) or by file
(`policy_file:`) in a provisioning manifest, or load it over the admin API
(`POST /v0/policies/{agent_id}`). An agent with no bundle runs under the
permissive (allow-all) engine in dev.

## Bundle format

```yaml
default_action: deny        # allow | deny (default: deny)
rules:
  - id: allow-llm
    priority: 5
    when:
      step_kind: llm_call
    then:
      decision: allow

  - id: allow-research-tools
    priority: 10
    when:
      targets: ["web_search", "doc_fetch", "calculator"]
    then:
      decision: allow
      reason: research tools are permitted
```

## Evaluation

Rules are sorted by **priority** (lowest number first). The first rule whose
`when` block matches wins. If none match, `default_action` applies. Priorities
must be unique within a bundle, and every rule needs an `id`.

## Conditions (`when`)

All fields present in a `when` block must match (AND). Omitted fields are not
checked.

| Field | Matches |
|-------|---------|
| `target` | The step target (tool name or model id). Supports glob patterns via Go's `path.Match` (`web_*`). |
| `targets` | A list of targets/globs; matches if any matches. |
| `agent_id` | The submitting agent's id. |
| `agent_ids` | A list of agent ids; matches if the agent is in it. |
| `step_kind` | `tool_call` or `llm_call`. |
| `arguments` | Predicates against fields inside the call's JSON arguments (see below). |

### Argument predicates

`arguments` is a map of argument key → predicate. The key must be present in the
call's arguments, and every listed constraint must pass. Values are compared as
strings.

```yaml
when:
  target: shell_exec
  arguments:
    command:
      regex: '^\s*(ls|cat|pwd|echo|whoami|date)(\s|$)'
```

| Constraint | Passes when the value… |
|------------|------------------------|
| `equals` | equals the string exactly |
| `contains` | contains the substring |
| `one_of` | is one of the listed strings |
| `regex` | matches the RE2 regular expression |

## Decisions (`then`)

| Field | Meaning |
|-------|---------|
| `decision` | `allow`, `deny`, or `require_approval`. **Required.** |
| `reason` | Human-readable explanation. Recorded in the decision event and returned on deny. |

Every policy decision event (`step.allowed`, `step.denied`,
`step.awaiting_approval`) carries the matched `rule_id` in its payload — that is
the audit trail.

### require_approval

When a rule returns `require_approval`, the kernel records `step.awaiting_approval`
and `approval.requested`, creates an approval, and transitions the execution to
`blocked`. A human resolves it via the approvals API (`grant` / `deny`), and the
execution resumes. See [events.md](events.md) and
[api.md](api.md#approvals).

```yaml
  - id: approve-fs-writes
    priority: 10
    when:
      targets: ["fs_write_*", "fs_edit_*"]
    then:
      decision: require_approval
      reason: filesystem writes need approval
```

## Examples

**Deny by default, allow only known tools:**

```yaml
default_action: deny
rules:
  - id: allow-llm
    priority: 5
    when: { step_kind: llm_call }
    then: { decision: allow }
  - id: allow-tools
    priority: 10
    when: { targets: ["web_search", "calculator"] }
    then: { decision: allow }
```

**Allow safe shell commands, gate the rest on approval:**

```yaml
default_action: deny
rules:
  - id: allow-llm
    priority: 5
    when: { step_kind: llm_call }
    then: { decision: allow }
  - id: allow-safe-shell
    priority: 10
    when:
      target: shell_exec
      arguments:
        command:
          regex: '^\s*(ls|cat|pwd|echo|whoami|date|uname|df|head|tail|wc)(\s|$)'
    then:
      decision: allow
      reason: safe read-only command
  - id: approve-other-shell
    priority: 20
    when: { target: shell_exec }
    then:
      decision: require_approval
      reason: non-safe shell command needs approval
```

See [`examples/policies/shell.yaml`](../examples/policies/shell.yaml) and
[`examples/rebuno.dev.yaml`](../examples/rebuno.dev.yaml) for working bundles.
