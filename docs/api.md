# API Reference

All endpoints are under the `/v0/` prefix. When a bearer token is configured on the kernel, all endpoints except `/v0/health` and `/v0/ready` require an `Authorization: Bearer <token>` header.

## Executions

### POST /v0/executions

Create a new execution.

**Request:**

```json
{
  "agent_id": "researcher",
  "input": {"task": "find recent papers on transformers"},
  "labels": {"env": "dev", "team": "research"}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_id` | string | Yes | Target agent ID |
| `input` | object | No | Input data for the execution |
| `labels` | object | No | Key-value metadata attached to the execution (usable in policy rules) |

**Response** (201 Created):

```json
{
  "id": "exec-abc123",
  "status": "pending",
  "agent_id": "researcher",
  "labels": {"env": "dev", "team": "research"},
  "input": {"task": "find recent papers on transformers"},
  "created_at": "2025-01-15T10:00:00Z",
  "updated_at": "2025-01-15T10:00:00Z"
}
```

### GET /v0/executions

List executions with optional filters and cursor-based pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | string | | Filter by execution status (`pending`, `running`, `blocked`, `completed`, `failed`, `cancelled`) |
| `agent_id` | string | | Filter by agent ID |
| `cursor` | string | | Pagination cursor from a previous response |
| `limit` | int | `50` | Maximum number of results (max 200) |

**Response** (200 OK):

```json
{
  "executions": [
    {
      "id": "exec-abc123",
      "status": "running",
      "agent_id": "researcher",
      "created_at": "2025-01-15T10:00:00Z",
      "updated_at": "2025-01-15T10:00:05Z"
    }
  ],
  "next_cursor": "eyJpZCI6ImV4ZWMtYWJjMTIzIn0="
}
```

### GET /v0/executions/{id}

Get the full state of a single execution.

**Response** (200 OK):

```json
{
  "id": "exec-abc123",
  "status": "running",
  "agent_id": "researcher",
  "labels": {"env": "dev"},
  "input": {"task": "find recent papers on transformers"},
  "output": null,
  "created_at": "2025-01-15T10:00:00Z",
  "updated_at": "2025-01-15T10:00:05Z"
}
```

### POST /v0/executions/{id}/cancel

Cancel an execution. Returns the updated execution state.

**Response** (200 OK):

```json
{
  "id": "exec-abc123",
  "status": "cancelled",
  "agent_id": "researcher",
  "labels": {"env": "dev"},
  "input": {"task": "find recent papers on transformers"},
  "output": null,
  "created_at": "2025-01-15T10:00:00Z",
  "updated_at": "2025-01-15T10:01:00Z"
}
```

### POST /v0/executions/{id}/signal

Send a signal to a blocked execution (e.g., human approval).

**Request:**

```json
{
  "signal_type": "approval",
  "payload": {"approved": true}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `signal_type` | string | Yes | Type of signal |
| `payload` | object | No | Signal payload data |

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

### GET /v0/executions/{id}/stream

SSE stream of execution events for real-time monitoring. This is a Server-Sent Events (SSE) endpoint, not a regular JSON endpoint. The connection remains open and events are pushed as they occur. The stream replays historical events first (after the specified sequence), then pushes new events in real time. The stream automatically closes when a terminal event (`execution.completed`, `execution.failed`, or `execution.cancelled`) is sent.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `after_sequence` | int | `0` | Only stream events with sequence greater than this value (useful for resuming) |

**Response:** SSE stream with `Content-Type: text/event-stream`.

Each event is formatted as a standard SSE message:

```
event: <event_type>
id: <sequence_number>
data: <JSON event object>
```

The JSON event object has the same structure as events returned by `GET /v0/executions/{id}/events`.

Periodic `:heartbeat` comments are sent to detect dead connections.

**Example:**

```
event: execution.created
id: 1
data: {"id":"550e8400-...","execution_id":"exec-abc123","type":"execution.created","sequence":1,...}

event: step.dispatched
id: 2
data: {"id":"661f9500-...","execution_id":"exec-abc123","type":"step.dispatched","sequence":2,...}

:heartbeat

event: execution.completed
id: 5
data: {"id":"772a0600-...","execution_id":"exec-abc123","type":"execution.completed","sequence":5,...}
```

### GET /v0/executions/{id}/events

List events for an execution with pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `after_sequence` | int | `0` | Return events with sequence greater than this value |
| `limit` | int | `100` | Maximum number of events (max 1000) |

**Response** (200 OK):

```json
{
  "events": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "execution_id": "exec-abc123",
      "step_id": "",
      "type": "execution.created",
      "schema_version": 1,
      "timestamp": "2025-01-15T10:00:00Z",
      "payload": {"agent_id": "researcher", "input": {}, "labels": {}},
      "causation_id": "550e8400-e29b-41d4-a716-446655440001",
      "correlation_id": "550e8400-e29b-41d4-a716-446655440002",
      "idempotency_key": "",
      "sequence": 1
    }
  ],
  "latest_sequence": 1
}
```

## Agents

### GET /v0/agents/stream

Open an SSE connection for execution assignment and event push.

**Query parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `agent_id` | string | Yes | Agent identifier |
| `consumer_id` | string | Yes | Unique identifier for this SSE connection instance |

**SSE events pushed:**

| Event Type | When | Payload |
|------------|------|---------|
| `execution.assigned` | Work assigned to this agent | Execution state, session, input, history |
| `tool.result` | Remote tool step completed/failed | step_id, status, result/error |
| `signal.received` | Signal delivered | signal_type, payload |

Periodic `:heartbeat` comments are sent to detect dead connections.

### POST /v0/agents/intent

Submit an intent for an execution.

**Request:**

```json
{
  "execution_id": "exec-123",
  "session_id": "sess-456",
  "intent": {
    "type": "invoke_tool",
    "tool_id": "web.search",
    "arguments": {"query": "latest news"},
    "idempotency_key": "exec-123:web.search:a1b2c3d4",
    "remote": false
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `execution_id` | string | Yes | Execution ID |
| `session_id` | string | Yes | Session ID |
| `intent.type` | string | Yes | `invoke_tool`, `complete`, `fail`, or `wait` |
| `intent.tool_id` | string | For `invoke_tool` | Tool to invoke |
| `intent.arguments` | object | No | Tool arguments |
| `intent.idempotency_key` | string | No | Deduplication key |
| `intent.remote` | bool | No | Whether to dispatch to a runner |
| `intent.output` | object | For `complete` | Execution output |
| `intent.error` | string | For `fail` | Error message |
| `intent.signal_type` | string | For `wait` | Signal type to wait for |

**Response** (200 OK):

```json
{
  "accepted": true,
  "step_id": "step-789"
}
```

On policy denial:

```json
{
  "accepted": false,
  "error": "Shell commands denied by default"
}
```

### POST /v0/agents/step-result

Report the result of a locally executed tool step.

**Request:**

```json
{
  "execution_id": "exec-123",
  "session_id": "sess-456",
  "step_id": "step-789",
  "success": true,
  "data": {"result": "search results here"}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `execution_id` | string | Yes | Execution ID |
| `session_id` | string | Yes | Session ID |
| `step_id` | string | Yes | Step ID |
| `success` | bool | Yes | Whether the tool succeeded |
| `data` | object | On success | Tool result data |
| `error` | string | On failure | Error message |

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

## Runners

### GET /v0/runners/stream

Open an SSE connection for job assignment.

**Query parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `runner_id` | string | Yes | Runner identifier |
| `consumer_id` | string | Yes | Unique identifier for this SSE connection instance |
| `capabilities` | string | No | Comma-separated list of tool IDs this runner can execute |

**SSE events pushed:**

| Event Type | When | Payload |
|------------|------|---------|
| `job.assigned` | Job dispatched to this runner | Job (id, execution_id, step_id, tool_id, arguments, deadline) |

Periodic `:heartbeat` comments are sent to detect dead connections. On disconnect, the runner is automatically unregistered. Each runner processes one job at a time -- the kernel tracks busy/idle state and dispatches accordingly.

### POST /v0/runners/{id}/results

Submit the result of a job execution.

**Request:**

```json
{
  "job_id": "job-xyz",
  "execution_id": "exec-123",
  "step_id": "step-789",
  "success": true,
  "data": {"results": [{"title": "..."}]},
  "started_at": "2025-01-15T10:00:01Z",
  "completed_at": "2025-01-15T10:00:03Z"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | Yes | Job ID |
| `execution_id` | string | Yes | Execution ID |
| `step_id` | string | Yes | Step ID |
| `success` | bool | Yes | Whether the tool succeeded |
| `data` | object | On success | Tool result data |
| `error` | string | On failure | Error message |
| `retryable` | bool | No | Whether the kernel should retry on failure |
| `started_at` | datetime | No | When execution started |
| `completed_at` | datetime | No | When execution completed |

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

### POST /v0/runners/steps/{stepId}/started

Mark a step as started by the runner.

**Request:**

```json
{
  "execution_id": "exec-123",
  "runner_id": "my-runner"
}
```

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

### POST /v0/runners/{id}/capabilities

Dynamically update the tool capabilities for a connected runner. Used by runners that discover tools at runtime (e.g., MCP-backed runners).

**Request:**

```json
{
  "tools": ["fs.read_file", "fs.write_file", "git.status"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tools` | string[] | Yes | New list of tool IDs this runner supports (replaces the previous list) |

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

### DELETE /v0/runners/{id}

Unregister a runner.

**Response** (204 No Content): Empty body.

## Observability

### GET /v0/health

Liveness check. Always returns 200 if the kernel process is running.

**Response** (200 OK):

```json
{
  "status": "ok"
}
```

### GET /v0/ready

Readiness check. Returns 200 if the database is reachable.

**Response** (200 OK):

```json
{
  "status": "ready"
}
```

**Response** (503 Service Unavailable):

```json
{
  "error": "service unavailable: database not reachable",
  "code": "SERVICE_UNAVAILABLE"
}
```

### GET /metrics

Prometheus metrics endpoint. Returns metrics in Prometheus exposition format.

## Error Responses

All errors return a JSON body:

```json
{
  "error": "human-readable message",
  "code": "VALIDATION_ERROR",
  "details": null
}
```

| Field | Type | Description |
|-------|------|-------------|
| `error` | string | Human-readable error message |
| `code` | string | Machine-readable error code |
| `details` | any | Additional error details (typically null) |

### Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `NOT_FOUND` | 404 | Resource not found |
| `CONFLICT` | 409 | State conflict (e.g., invalid state transition, step already resolved, execution tainted) |
| `VALIDATION_ERROR` | 400 | Invalid request (missing fields, bad JSON, etc.) |
| `FORBIDDEN` | 403 | Policy denied the request |
| `UNAUTHORIZED` | 401 | Missing or invalid bearer token, or expired session |
| `RATE_LIMITED` | 429 | Rate limit exceeded |
| `INTERNAL_ERROR` | 500 | Unexpected kernel error |
| `SERVICE_UNAVAILABLE` | 503 | Kernel dependency unavailable (e.g., database down) |
