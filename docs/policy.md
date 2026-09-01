# Policy

Policy decides which effects an agent may perform. Each agent has its own rule
bundle, evaluated the first time a step is submitted. A rule returns `allow`,
`deny`, or `require_approval`.

A replayed step returns its recorded outcome without consulting policy again, so
editing a bundle does not re-gate work an execution has already done.

Attach a bundle inline with `policy:` or from a file with `policy_file:` in a
provisioning manifest. Set one or the other, not both, and write the
`policy_file` path relative to the manifest. You can also load a bundle over the
admin API with `POST /v0/policies/{agent_id}`.

The kernel compiles a bundle before storing it, so a malformed one is rejected
with a validation error at registration rather than failing later at a step. An
agent with no bundle is unrestricted, in production as much as in dev.

## Bundle format

```yaml
default_action: deny        # allow | deny (default: deny)
rules:
  - id: allow-llm
    when:
      step_kind: llm_call
    then:
      decision: allow

  - id: allow-research-tools
    when:
      targets: ["web_search", "doc_fetch", "calculator"]
    then:
      decision: allow
      reason: research tools are permitted
```

## Evaluation

Rules are evaluated top to bottom in the order they appear in the bundle. The
first rule whose `when` block matches wins, so put the specific rules above the
broad ones. If nothing matches, `default_action` applies, and any value other
than `allow` means deny. Every rule needs an `id`, unique within the bundle.
Unknown keys are rejected, so a bundle carrying a key no rule field claims will
not load.

`default_action` covers `tool_call` and `llm_call` steps. An unmatched `local`
step is allowed whatever `default_action` says. To govern one, match it with a
rule on `target` or `step_kind: local`.

## Match conditions

All fields present in a rule's `when` block must match (AND). Omitted fields are
not checked.

| Field | Matches |
|-------|---------|
| `target` | The step target (tool name or model id). Supports glob patterns via Go's `path.Match` (`web_*`). |
| `targets` | A list of targets/globs; matches if any matches. |
| `agent_id` | The submitting agent's id. |
| `agent_ids` | A list of agent ids; matches if the agent is in it. |
| `step_kind` | `tool_call`, `llm_call`, or `local`. |
| `arguments` | Predicates against fields inside the call's JSON arguments. See [Argument predicates](#argument-predicates). |

### Argument predicates

`arguments` is a map of argument key to predicate. The key must be present in
the call's arguments, and every constraint listed under it must pass. Values are
compared as strings.

A predicate needs at least one constraint. An empty one (`command: {}`, or
`command: {equals: ""}`) is rejected at load, because a constraint that matches
any value would silently widen the rule.

```yaml
when:
  target: shell_exec
  arguments:
    command:
      regex: '^\s*(ls|cat|pwd|echo|whoami|date)(\s+[^;&|<>$`()\\]*)?\s*$'
```

| Constraint | Passes when the value… |
|------------|------------------------|
| `equals` | equals the string exactly |
| `contains` | contains the substring |
| `one_of` | is one of the listed strings |
| `regex` | matches the RE2 regular expression |

## Rule decisions

A rule's `then` block carries the decision and its options:

| Field | Meaning |
|-------|---------|
| `decision` | `allow`, `deny`, or `require_approval`. Required. |
| `reason` | Human-readable explanation. Recorded in the decision event and returned on deny. |
| `approval_config` | Who approves, and for how long. Only meaningful with `require_approval`. See [Approvals](#approvals). |
| `rate_limit` | Caps how often the rule may fire. See [Rate limits](#rate-limits). |
| `budget` | Caps the LLM tokens the execution may spend. See [Token budgets](#token-budgets). |

Every policy decision event (`step.allowed`, `step.denied`,
`step.awaiting_approval`) carries the matched `rule_id` in its payload, which is
what makes the log auditable. `rule_id` is the matched rule's own `id` and is
not settable from the bundle. A decision that came from somewhere other than a
rule gets a fixed id. The `default_action` fallthrough is `default`, an
unmatched local step is `local`, an agent with no bundle is `permissive`, and a
stored bundle that will not compile is `bundle-error`.

### Approvals

When a rule returns `require_approval`, the kernel records
`step.awaiting_approval` and `approval.requested`, creates an approval, and
moves the execution to `blocked`. A human grants or denies it through the
approvals API and the execution resumes. See [Events](events.md) and
[Approvals](api.md#approvals).

```yaml
  - id: approve-fs-writes
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
| `approvers` | Who may grant or deny. A decision whose `decided_by` is not in the list is rejected with `403 forbidden`. Omit the field, or leave it empty, to let anyone decide. That is the default. It is a guardrail, not access control. |
| `timeout` | A Go duration (`30s`, `5m`, `1h30m`). The approval expires after it. Defaults to the kernel's configured timeout. |
| `message` | Shown to whoever resolves the approval. |

`approvers` is a guardrail rather than access control. `decided_by` is a string
in the request body, and the bearer token is shared and carries no identity, so
the check stops the wrong person deciding by accident but not someone willing to
type another person's name. Enforcing it properly needs `decided_by` to come
from an authenticated principal. Do not rely on `approvers` to keep a decision
away from a caller who already holds the API token.

### Rate limits

A rule can cap how often it fires. The bucket is keyed on the rule's `rule_id`
and the scope in `per_what`, so two rules never share one.

```yaml
  - id: limit-search
    when:
      target: web_search
    then:
      decision: allow
      rate_limit:
        max_calls: 5
        window: 1m
        per_what: execution      # execution (default) | agent | global
        max_wait: 5m             # unset (default) refuses instead of waiting
        on_limiter_error: allow  # allow (default, fail-open) | deny (fail-closed)
```

A step over the limit comes back as `rate_limited` rather than a policy denial.
Put a hard ceiling in a `deny` or `require_approval` rule instead.

With `max_wait` set, a limited step parks. The submit returns `blocked`, the
execution stays `running`, and the kernel re-dispatches once the bucket refills.
An execution parks only once. If the step is still over the limit on the retry,
or the wait would run longer than `max_wait`, it is refused.

### Token budgets

A rule can cap the LLM tokens an execution spends. The cap applies only to a
rule that decides `allow`.

```yaml
  - id: cap-llm-spend
    when: { step_kind: llm_call }
    then:
      decision: allow
      budget:
        max_tokens: 200000
        on_exceed: deny        # deny (default) | require_approval
```

The meter sums the input and output tokens recorded on the execution's steps, so
an effect counts once however many times it was attempted. Only `llm_call` steps
record usage, so nothing else moves the meter.

The check runs before the call, so the step that crosses the limit still runs. A
response with no parseable usage never advances the meter, most often an
OpenAI-style stream requested without `stream_options.include_usage`, and
`rebuno_llm_usage_missing_total` counts those. If the kernel cannot read the
execution's usage at all, it lets the step through.

## Testing a bundle

`rebuno policy test` evaluates a bundle against cases and exits non-zero when a
decision does not match what the case expects.

```bash
rebuno policy test examples/policies/shell.yaml
```

Cases live beside the bundle, `shell.yaml` with `shell.policytest.yaml`, or
wherever `--cases` points.

```yaml
agent_id: shell             # inherited by cases that do not name one
cases:
  - name: read-only command
    target: shell_exec
    args: { command: "ls -la /tmp" }
    expect: allow
    expect_rule: allow-safe-shell
  - name: chained command
    target: shell_exec
    args: { command: "ls /tmp; rm -rf /" }
    expect: require_approval
```

`target` is required and `kind` defaults to `tool_call`. `expect` is the
decision the case must produce and `expect_rule` the rule that must make it,
which catches a case still decided correctly but by the wrong rule. Omit both to
assert nothing. Unknown keys are rejected. A run also lists the rules no case
reached, which is how a rule shadowed by a broader one above it shows up.

`--target <name> --args '{...}'` probes a single input instead of running cases,
printing its decision and rule.

A finished execution is the other source of cases. Replaying one feeds its
recorded steps back through a bundle and fails the steps a change would now
decide differently. The steps come from a running kernel, so replay names an
agent with `--agent-id` and reaches a kernel the way every command does
([CLI](cli.md)), or calls
[`POST /v0/policies/{agent_id}/test`](api.md#policy) directly.

```bash
rebuno policy test shell.yaml --execution <id> --agent-id shell
```

## Examples

**Deny by default, allow only known tools:**

```yaml
default_action: deny
rules:
  - id: allow-llm
    when: { step_kind: llm_call }
    then: { decision: allow }
  - id: allow-tools
    when: { targets: ["web_search", "calculator"] }
    then: { decision: allow }
```

**Allow safe shell commands, gate the rest on approval:**

```yaml
default_action: deny
rules:
  - id: allow-llm
    when: { step_kind: llm_call }
    then: { decision: allow }
  - id: allow-safe-shell
    when:
      target: shell_exec
      arguments:
        command:
          regex: '^\s*(ls|cat|pwd|echo|whoami|date|uname|df|head|tail|wc)(\s+[^;&|<>$`()\\]*)?\s*$'
    then:
      decision: allow
      reason: safe read-only command
  - id: approve-other-shell
    when: { target: shell_exec }
    then:
      decision: require_approval
      reason: non-safe shell command needs approval
```

See [`examples/policies/shell.yaml`](../examples/policies/shell.yaml) and
[`examples/rebuno.dev.yaml`](../examples/rebuno.dev.yaml) for working bundles.
