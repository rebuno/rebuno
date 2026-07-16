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

A predicate must carry at least one constraint. A bundle with an empty predicate
(`command: {}`, or `command: {equals: ""}`) is rejected at load — an empty
constraint would match any value and silently widen the rule.

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
| `approval_config` | Who approves, and for how long. Only meaningful with `require_approval` (see below). |
| `rate_limit` | Caps how often the rule may fire (see below). |

Every policy decision event (`step.allowed`, `step.denied`,
`step.awaiting_approval`) carries the matched `rule_id` in its payload — that is
the audit trail. `rule_id` is always the matched rule's own `id`; it is not
settable from the bundle.

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
      approval_config:
        approvers: ["alice", "bob"]   # omit to let anyone decide
        timeout: 5m                   # default: the kernel's DefaultApprovalTimeout
        message: check the target path before granting
```

| Field | Meaning |
|-------|---------|
| `approvers` | Who may grant or deny. A decision whose `decided_by` is not in the list is rejected with `403 forbidden`. Omit the field (or leave it empty) to let anyone decide — that is the default. **Not access control:** see below. |
| `timeout` | A Go duration (`30s`, `5m`, `1h30m`). The approval expires after it. Defaults to the kernel's configured timeout. |
| `message` | Shown to whoever resolves the approval. |

`approvers` is a guardrail, not access control. `decided_by` is a string in the
request body and the bearer token is shared and carries no identity, so the
check stops the wrong person deciding — not someone who types another person's
name. Enforcing it properly requires `decided_by` to come from an authenticated
principal. Do not rely on `approvers` to keep a decision away from a caller who
already holds the API token.

### rate_limit

A rule may cap how often it fires. The limit is keyed on the rule's `rule_id`
and the scope in `per_what`, so two rules never share a bucket.

```yaml
  - id: limit-search
    priority: 10
    when:
      target: web_search
    then:
      decision: allow
      rate_limit:
        max_calls: 5
        window: 1m
        per_what: execution      # execution (default) | agent | global
        on_limiter_error: allow  # allow (default, fail-open) | deny (fail-closed)
```

A step over the limit is rejected with `rate_limited` rather than denied by
policy. A hard ceiling belongs in a `deny` or `require_approval` rule instead.

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
