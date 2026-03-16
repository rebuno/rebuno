# Event Types

Every action in the rebuno kernel produces events that form an immutable audit trail. Events are persisted in the event store and can be queried via the API or the `rebuno` CLI.

## Event Structure

Every event has the following fields:

| Field | Type | Description |
|-------|------|-------------|
| `id` | UUID | Unique event identifier |
| `execution_id` | string | The execution this event belongs to |
| `step_id` | string | Associated step ID (empty for execution-level events) |
| `type` | string | Event type (see below) |
| `schema_version` | int | Payload schema version |
| `timestamp` | datetime | When the event was created |
| `payload` | object | Type-specific payload (see below) |
| `causation_id` | UUID | ID of the event or action that caused this event |
| `correlation_id` | UUID | Shared ID linking a chain of related events |
| `idempotency_key` | string | Deduplication key |
| `sequence` | int | Monotonically increasing sequence number within the execution |

## Event Categories

Events are classified into three categories:

- **State-mutating**: Change the state of an execution or step. These are the primary events that drive the state machine.
- **Decisional**: Record policy decisions (accept/deny) on intents.
- **Informational**: Record notable occurrences that do not change state.

## Persisted vs. Transient Events

**Persisted events** are stored in the event store and can be queried via `GET /v0/executions/{id}/events`. All event types listed below are persisted.

**Transient SSE push events** are sent over Server-Sent Events connections but are not stored in the event store:

| Event Type | Pushed To | Description |
|------------|-----------|-------------|
| `execution.assigned` | Agent SSE | Notifies an agent that an execution has been assigned to it |
| `tool.result` | Agent SSE | Delivers a remote tool step result to the agent |
| `signal.received` | Agent SSE | Delivers a signal to the agent |
| `approval.resolved` | Agent SSE | Notifies the agent that an approval request has been resolved |
| `job.assigned` | Runner SSE | Dispatches a tool execution job to a runner |

These transient events are delivery mechanisms. The underlying state changes are captured by persisted events (e.g., `execution.started`, `step.completed`).

## Execution Events

### execution.created

Emitted when a new execution is created via `POST /v0/executions`.

```json
{
  "agent_id": "researcher",
  "input": {"query": "what is event sourcing?"},
  "labels": {"env": "dev"}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `agent_id` | string | Target agent ID |
| `input` | object | Execution input data |
| `labels` | object | Key-value labels attached to the execution |

### execution.started

Emitted when the kernel assigns an execution to an agent via SSE.

```json
{
  "session_id": "sess-abc123",
  "consumer_id": "researcher-x8f2k"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `session_id` | string | The session created for this assignment |
| `consumer_id` | string | The SSE consumer that received the assignment |

### execution.blocked

Emitted when an execution enters the blocked state, waiting for a tool result or signal.

```json
{
  "reason": "tool",
  "ref": "step-abc123"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Why the execution is blocked: `"tool"`, `"signal"`, or `"approval"` |
| `ref` | string | Reference: step ID (for tool/approval) or signal type (for signal) |
| `tool_id` | string | Tool being invoked (set when reason=`"approval"`) |
| `arguments` | object | Tool arguments (set when reason=`"approval"`) |
| `remote` | bool | Whether the tool is remote (set when reason=`"approval"`) |

### execution.resumed

Emitted when a blocked execution returns to running state.

```json
{
  "reason": "step completed"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Why the execution was resumed |

### execution.completed

Emitted when an agent submits a `complete` intent.

```json
{
  "output": {"answer": "Event sourcing is..."}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `output` | object | The execution's output data |

### execution.failed

Emitted when an execution fails (agent submits `fail` intent, timeout, etc.).

```json
{
  "error": "Tool execution failed: web.search returned 500"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `error` | string | Human-readable error message |

### execution.reset

Emitted when the kernel resets an execution back to pending (e.g., after agent disconnect or recovery).

```json
{
  "reason": "agent_disconnect",
  "from_status": "running"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Why the execution was reset (e.g., `"agent_disconnect"`, `"recovery"`) |
| `from_status` | string | The execution status before the reset |

### execution.cancelled

Emitted when an execution is cancelled via `POST /v0/executions/{id}/cancel`.

```json
{
  "reason": "user requested cancellation"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Why the execution was cancelled |

## Intent Events

### intent.accepted

Emitted when the kernel accepts an intent (policy allows it or it is a lifecycle intent).

```json
{
  "intent_type": "invoke_tool",
  "details": {"tool_id": "web.search", "step_id": "step-abc123"}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `intent_type` | string | The intent type: `invoke_tool`, `complete`, `fail`, `wait` |
| `details` | object | Additional details about the accepted intent (optional) |

### intent.denied

Emitted when the kernel denies an intent due to policy.

```json
{
  "intent_type": "invoke_tool",
  "tool_id": "shell.exec",
  "arguments": {"command": "rm -rf /"},
  "idempotency_key": "exec-123:shell.exec:a1b2c3d4",
  "reason": "Shell commands denied by default",
  "rule_id": "deny-shell"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `intent_type` | string | The intent type that was denied |
| `tool_id` | string | The tool that was requested (for `invoke_tool` intents) |
| `arguments` | object | The arguments that were submitted (for `invoke_tool` intents) |
| `idempotency_key` | string | The idempotency key from the request |
| `reason` | string | Human-readable denial reason |
| `rule_id` | string | The ID of the policy rule that caused the denial |

## Step Events

### step.created

Emitted when a tool invocation intent is accepted and a step is created.

```json
{
  "tool_id": "web.search",
  "tool_version": 1,
  "arguments": {"query": "event sourcing"},
  "max_attempts": 3,
  "attempt": 1
}
```

| Field | Type | Description |
|-------|------|-------------|
| `tool_id` | string | Tool being invoked |
| `tool_version` | int | Tool version |
| `arguments` | object | Arguments passed to the tool |
| `max_attempts` | int | Maximum retry attempts |
| `attempt` | int | Current attempt number |

### step.dispatched

Emitted when the kernel dispatches a step to a runner for remote execution.

```json
{
  "runner_id": "my-runner",
  "job_id": "job-xyz789",
  "deadline": "2025-01-15T10:30:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `runner_id` | string | The runner the job was dispatched to |
| `job_id` | string | Unique job identifier |
| `deadline` | datetime | When the step times out |

### step.started

Emitted when a runner reports that it has begun executing the step.

```json
{
  "runner_id": "my-runner"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `runner_id` | string | The runner executing the step |

### step.completed

Emitted when a step finishes successfully.

```json
{
  "result": {"results": [{"title": "Event Sourcing", "url": "..."}]}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `result` | object | The tool's return value |

### step.failed

Emitted when a step fails.

```json
{
  "error": "web.search returned HTTP 500",
  "retryable": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `error` | string | Error message |
| `retryable` | bool | Whether the kernel should retry this step |

### step.timed_out

Emitted when a step exceeds its deadline. The payload is empty.

```json
{}
```

### step.retried

Emitted when a failed step is retried.

```json
{
  "next_attempt": 2
}
```

| Field | Type | Description |
|-------|------|-------------|
| `next_attempt` | int | The attempt number of the retry |

### step.cancelled

Emitted when a step is cancelled (e.g., when the parent execution is cancelled).

```json
{
  "reason": "execution cancelled"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Why the step was cancelled |

### step.approval_required

Emitted when a step requires human approval before it can proceed. This is an informational event.

```json
{
  "tool_id": "deploy.production",
  "arguments": {"service": "api", "version": "v2.1.0"},
  "remote": false,
  "reason": "Production deployments require approval"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `tool_id` | string | Tool that requires approval |
| `arguments` | object | Tool arguments submitted |
| `remote` | bool | Whether the tool is remote |
| `reason` | string | Human-readable reason from the policy rule |

## Other Events

### signal.received

Emitted when a signal is delivered to a blocked execution via `POST /v0/executions/{id}/signal`.

```json
{
  "signal_type": "approval",
  "payload": {"approved": true, "reviewer": "alice"}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `signal_type` | string | The type of signal |
| `payload` | object | Signal payload data |

### agent.timeout

Emitted when an agent's session times out due to connectivity loss.

```json
{
  "session_id": "sess-abc123"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `session_id` | string | The session that timed out |
