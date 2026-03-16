# Agents

Agents are processes that receive executions via SSE, call tools, and produce results. They maintain a persistent SSE connection to the kernel and submit intents via HTTP.

## Lifecycle

1. **Connect** -- Agent opens an SSE connection to the kernel. The kernel assigns pending executions by pushing `execution.assigned` events.
2. **Intent** -- Agent submits intents (invoke tool, complete, fail, wait). Each intent is policy-checked and produces events.
3. **Step Result** -- After executing a tool locally, the agent reports the result via HTTP.
4. **Disconnect** -- If the SSE connection drops, the kernel cleans up the session and returns the execution to pending state for reassignment.

## API

### Connect (SSE)

```
GET /v0/agents/stream?agent_id=researcher&consumer_id=instance-1
Accept: text/event-stream
```

Opens a persistent SSE connection. The kernel pushes events:

| Event Type | When | Payload |
|------------|------|---------|
| `execution.assigned` | Work assigned to this agent | Execution state, session, input, history |
| `tool.result` | Remote tool step completed/failed | step_id, status, result/error |
| `signal.received` | Signal delivered | signal_type, payload |

Periodic `:heartbeat` comments are sent to detect dead connections.

### Submit Intent

```
POST /v0/agents/intent
{
  "execution_id": "exec-123",
  "session_id": "sess-456",
  "intent": {
    "type": "invoke_tool",
    "tool_id": "web.search",
    "arguments": {"query": "latest news"}
  }
}
```

Intent types:

| Type | Purpose | Required Fields |
|------|---------|-----------------|
| `invoke_tool` | Call a tool | `tool_id`, optionally `arguments`, `idempotency_key`, `remote` |
| `complete` | Finish execution | `output` (JSON) |
| `fail` | Fail execution | `error` (string) |
| `wait` | Wait for signal | `signal_type` |

### Report Step Result

After executing a tool locally:

```
POST /v0/agents/step-result
{
  "execution_id": "exec-123",
  "session_id": "sess-456",
  "step_id": "step-789",
  "success": true,
  "data": {"result": "search results here"}
}
```

## Consumer ID

The `consumer_id` query parameter on the SSE connection identifies a specific SSE connection instance for a given agent or runner.

- **Multiple consumers**: Multiple processes can connect with the same `agent_id` but different `consumer_id` values. This provides redundancy and horizontal scaling for a single agent type.
- **Round-robin assignment**: The kernel round-robins execution assignments across all connected consumers for the same `agent_id`. If two consumers are connected as `agent_id=researcher`, each gets roughly half the executions.
- **Uniqueness**: Each `consumer_id` must be unique per active connection. Connecting with a `consumer_id` that is already in use will replace the previous connection.
- **Auto-generation**: The Python SDK auto-generates `consumer_id` as `{agent_id}-{random}` if not explicitly provided.

```
# Two instances of the same agent for load distribution
GET /v0/agents/stream?agent_id=researcher&consumer_id=researcher-instance-1
GET /v0/agents/stream?agent_id=researcher&consumer_id=researcher-instance-2
```

## Sessions

A session is created when the kernel assigns an execution to an agent via SSE. It ties the agent's SSE connection to the execution. If the SSE connection drops, the session is deleted and the execution is returned to pending state for reassignment.

## Remote Tool Execution

Set `"remote": true` on an `invoke_tool` intent to dispatch the tool call to a runner instead of executing locally. The kernel pushes a `job.assigned` event to an idle runner with the matching capability via SSE. Runners maintain a persistent SSE connection (`GET /v0/runners/stream`) and process one job at a time.

## Timeouts

The kernel supports two timeout layers:

| Timeout | Default | Description |
|---------|---------|-------------|
| `StepTimeout` | 5 min | Default deadline for a single tool step. Can be overridden per policy rule via `timeout_ms`. |
| `ExecutionTimeout` | 1 hour | Maximum wall-clock time for an entire execution. |

Agent connectivity is monitored via the SSE connection. If the connection drops, the kernel handles disconnect and returns the execution to pending state. The `--agent-timeout` configuration controls the session expiry grace period.

Policy rules can set `timeout_ms` in their `then` block to override the global step timeout for matching tool invocations. See [Policy](policy.md) for details.
