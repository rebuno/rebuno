# Policy

The policy engine controls which tool invocations are allowed. Rules are defined in YAML and evaluated on every `tool.invoke` intent. The `--policy` flag accepts a single file or a directory of files.

## File Format

```yaml
rules:
  - id: "rule-name"
    priority: 10
    when:
      # conditions
    then:
      decision: "allow"   # or "deny"
      reason: "optional explanation"
      timeout_ms: 30000   # optional per-rule step timeout

default:
  decision: "deny"
  reason: "No explicit allow rule matched"
```

## Rule Evaluation

Rules are sorted by **priority** (lowest number first). The first rule whose `when` block matches the input wins. If no rule matches, the `default` action applies.

Each rule ID must be unique. Each priority value must be unique.

## Conditions

All conditions in a `when` block must match (AND logic). Omitted fields are not checked.

### action

Match the intent action. Currently only `tool.invoke` is policy-checked.

```yaml
when:
  action: "tool.invoke"
```

### tool_id / tool_ids

Match by tool identifier. Supports glob patterns using Go's `path.Match` semantics, where `*` matches any sequence of non-`/` characters. Note that `*` is not limited to dot-separated segments -- `web.*` matches `web.search`, `web.fetch`, and also `web.search.deep`.

```yaml
when:
  tool_id: "web.*"        # matches web.search, web.fetch, etc.

when:
  tool_ids: ["web.*", "doc.*"]   # match any in the list
```

### agent_id / agent_ids

Match by the agent submitting the intent.

```yaml
when:
  agent_id: "researcher"

when:
  agent_ids: ["researcher", "analyst"]
```

### labels

Match execution labels. All specified labels must match (AND).

```yaml
when:
  labels:
    env: "prod"
    team: "search"
```

### arguments

Match fields inside the tool's JSON arguments. Each predicate targets a top-level field. All predicates must pass (AND logic).

```yaml
when:
  arguments:
    - field: "command"
      pattern: "^(ls|cat|echo|pwd)"
    - field: "timeout"
      max: 30
```

#### Argument Predicate Fields

| Field | Type | Description |
|-------|------|-------------|
| `field` | string | **Required.** Top-level key in the arguments JSON. |
| `pattern` | string | Regex the string value must match. |
| `one_of` | string[] | Value must be one of these strings. |
| `min` | number | Numeric value must be >= this. |
| `max` | number | Numeric value must be <= this. |
| `max_length` | int | String length must be <= this. |
| `required` | bool | Field must exist and be non-empty. |

Each predicate must have `field` set and at least one constraint.

**Missing fields:** If a field is absent from the arguments and `required` is not set, the predicate is skipped (no violation). If `required: true`, a missing or empty field causes the predicate to fail.

**Type mismatches:** `pattern`, `one_of`, and `max_length` require string values. `min` and `max` require numeric values. A type mismatch fails the predicate.

## Actions

The `then` block specifies what happens when a rule matches.

| Field | Type | Description |
|-------|------|-------------|
| `decision` | string | **Required.** `"allow"`, `"deny"`, or `"require_approval"`. |
| `reason` | string | Human-readable explanation. Included in deny responses and events. |
| `timeout_ms` | int | Step timeout in milliseconds for this rule. Overrides the global `StepTimeout` (default: 5 min). Only meaningful for `allow` rules. |

### Approval Flow

When a rule returns `require_approval`, the kernel creates the step but blocks the execution until a human sends an approval signal via `POST /v0/executions/{id}/signal`. The execution emits a `step.approval_required` event and transitions to the `blocked` state. Once approved, the step proceeds to execution (local or remote). If denied, the step is cancelled.

```yaml
- id: "approve-deploy"
  priority: 15
  when:
    action: "tool.invoke"
    tool_id: "deploy.*"
  then:
    decision: "require_approval"
    reason: "Production deployments require human approval"
```

### Per-Rule Timeouts

When `timeout_ms` is set on an allow rule, the kernel uses it as the step deadline instead of the global `StepTimeout`. This gives operators fine-grained control over how long individual tool invocations can run, scoped by any combination of agent, tool, labels, and arguments.

```yaml
# Web tools get 30 seconds
- id: "researcher-web-tools"
  priority: 10
  when:
    agent_ids: ["researcher"]
    tool_ids: ["web.*"]
  then:
    decision: "allow"
    timeout_ms: 30000

# Shell commands get 10 seconds
- id: "allow-safe-shell"
  priority: 30
  when:
    tool_id: "shell.exec"
    arguments:
      - field: "command"
        pattern: "^(ls|cat|echo|pwd)"
  then:
    decision: "allow"
    timeout_ms: 10000

# Long-running data processing gets 10 minutes
- id: "data-pipeline"
  priority: 50
  when:
    tool_id: "data.transform"
    labels:
      pipeline: "etl"
  then:
    decision: "allow"
    timeout_ms: 600000
```

If `timeout_ms` is `0` or omitted, the global `StepTimeout` applies.

## Examples

Allow a specific agent to use web tools with a 30-second timeout:

```yaml
- id: "researcher-web-tools"
  priority: 10
  when:
    action: "tool.invoke"
    agent_ids: ["researcher", "researcher-local"]
    tool_ids: ["web.*", "doc.*"]
  then:
    decision: "allow"
    timeout_ms: 30000
```

Allow shell commands only if they start with safe prefixes and have a bounded timeout:

```yaml
- id: "allow-safe-shell"
  priority: 30
  when:
    action: "tool.invoke"
    tool_id: "shell.exec"
    arguments:
      - field: "command"
        pattern: "^(ls|cat|echo|pwd|whoami|date|head|tail|wc)"
      - field: "timeout"
        max: 30
  then:
    decision: "allow"
    reason: "Safe read-only shell commands allowed"
    timeout_ms: 10000

- id: "deny-shell"
  priority: 40
  when:
    action: "tool.invoke"
    tool_id: "shell.exec"
  then:
    decision: "deny"
    reason: "Shell commands denied by default"
```

## Directory-Based Policy

When `--policy` points to a directory, the engine loads per-agent policy files and an optional global policy file. The filename (minus extension) is the agent ID.

```bash
rebuno server --policy examples/policies/demo/
```

### How It Works

1. Each file is named after the agent it governs: `researcher.yaml`, `researcher-local.yaml`, etc.
2. If a `_global.yaml` (or `_global.yml`) file exists, its rules apply across all agents. Use `agent_ids` in global rules to scope them to specific agents.
3. Global rules are merged into each per-agent engine. Priority ordering works across both — a global rule at priority 100 and an agent rule at priority 10 are sorted together.
4. Agents without a per-agent file are evaluated against the global rules only. If there is no global file either, the request is denied.
5. Each file is parsed and validated independently. Rule IDs and priorities must be unique within the merged set (per-agent rules + global rules).

### Example Layout

```
examples/policies/demo/
  _global.yaml            # shared rules for all agents
  researcher.yaml         # agent-specific rules for researcher
  researcher-local.yaml   # agent-specific rules for researcher-local
```

`_global.yaml` — shared rules with `agent_ids` scoping:
```yaml
rules:
  - id: "research-agents-web"
    priority: 10
    when:
      action: "tool.invoke"
      agent_ids: ["researcher", "researcher-local"]
      tool_ids: ["web.*", "doc.*"]
    then:
      decision: "allow"
      timeout_ms: 30000

  - id: "allow-safe-shell"
    priority: 30
    when:
      action: "tool.invoke"
      tool_id: "shell.exec"
      arguments:
        - field: "command"
          pattern: "^(ls|cat|echo|pwd)"
    then:
      decision: "allow"
      timeout_ms: 10000

  - id: "deny-shell"
    priority: 40
    when:
      action: "tool.invoke"
      tool_id: "shell.exec"
    then:
      decision: "deny"
      reason: "Shell commands denied by default"

default:
  decision: "deny"
  reason: "No explicit allow rule matched"
```

`researcher.yaml` — agent-specific overrides:
```yaml
rules:
  - id: "calculator"
    priority: 20
    when:
      action: "tool.invoke"
      tool_id: "calculator"
    then:
      decision: "allow"
```

In this setup, the `researcher` agent gets its own `calculator` rule merged with all the global rules. The `researcher-local` agent has no per-agent file, so it is evaluated against the global rules only — it can use `web.*` and `doc.*` tools (via the `agent_ids` scoping) but not the calculator.

## Secure Defaults

The `SecureDefaultEngine` wrapper automatically allows execution lifecycle actions (`execution.complete`, `execution.fail`, `execution.wait`) and delegates `tool.invoke` to the rule engine. If no policy is configured, all tool invocations are denied.
