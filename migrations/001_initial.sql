-- +goose Up

CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    webhook_url TEXT NOT NULL,
    secret TEXT NOT NULL,
    policy_bundle TEXT NOT NULL DEFAULT '',
    lease_timeout_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    registered_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE executions (
    id UUID PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    input JSONB COMPRESSION lz4 NOT NULL,
    status TEXT NOT NULL,
    output JSONB COMPRESSION lz4,
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deadline_at TIMESTAMPTZ
);
CREATE INDEX executions_deadline_idx ON executions (deadline_at) WHERE status IN ('pending','running','blocked');

CREATE INDEX executions_agent_id_idx ON executions (agent_id, id DESC);

CREATE TABLE events (
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    event_seq BIGINT NOT NULL,
    type TEXT NOT NULL,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (execution_id, event_seq)
);

CREATE TABLE steps (
    step_id TEXT PRIMARY KEY,
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    target TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    occurrence INT NOT NULL,
    status TEXT NOT NULL,
    idempotency TEXT NOT NULL DEFAULT 'safe_to_retry',
    args JSONB COMPRESSION lz4,
    result JSONB COMPRESSION lz4,
    error JSONB COMPRESSION lz4,
    usage_input INT NOT NULL DEFAULT 0,
    usage_output INT NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (execution_id, kind, target, args_hash, occurrence)
);

CREATE TABLE approvals (
    id UUID PRIMARY KEY,
    step_id TEXT NOT NULL REFERENCES steps(step_id) ON DELETE CASCADE,
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    approvers JSONB,
    message TEXT NOT NULL DEFAULT '',
    timeout_at TIMESTAMPTZ NOT NULL,
    decided_by TEXT NOT NULL DEFAULT '',
    decided_at TIMESTAMPTZ,
    rationale TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX approvals_pending_timeout_idx ON approvals (status, timeout_at) WHERE status = 'pending';
CREATE INDEX approvals_execution_idx ON approvals (execution_id);
CREATE INDEX approvals_step_idx ON approvals (step_id);

CREATE TABLE dispatches (
    id UUID PRIMARY KEY,
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    attempt INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by TEXT,
    locked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX dispatches_execution_idx ON dispatches (execution_id);
CREATE INDEX dispatches_due_idx ON dispatches (next_attempt_at) WHERE status IN ('pending','failed');
CREATE INDEX dispatches_lease_idx ON dispatches (locked_at) WHERE status = 'in_flight';

CREATE TABLE dispatch_step_counters (
    dispatch_id UUID NOT NULL REFERENCES dispatches(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    target TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    consumed INT NOT NULL,
    PRIMARY KEY (dispatch_id, kind, target, args_hash)
);

CREATE TABLE rate_buckets (
    key            TEXT PRIMARY KEY,
    tokens         DOUBLE PRECISION NOT NULL,
    max_tokens     INTEGER NOT NULL,
    window_seconds DOUBLE PRECISION NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX rate_buckets_updated_idx ON rate_buckets (updated_at);
