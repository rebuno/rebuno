CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    webhook_url TEXT NOT NULL,
    secret TEXT NOT NULL,
    policy_bundle TEXT,
    registered_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS policy_bundle TEXT;

CREATE TABLE IF NOT EXISTS executions (
    id UUID PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    agent_version TEXT,
    input JSONB NOT NULL,
    status TEXT NOT NULL,
    output JSONB,
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deadline_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS executions_active_idx ON executions (status) WHERE status IN ('pending','running','blocked');

CREATE INDEX IF NOT EXISTS executions_agent_id_idx ON executions (agent_id, id DESC);

CREATE TABLE IF NOT EXISTS events (
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    event_seq BIGINT NOT NULL,
    type TEXT NOT NULL,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (execution_id, event_seq)
);

CREATE TABLE IF NOT EXISTS steps (
    step_id TEXT PRIMARY KEY,
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    target TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    occurrence INT NOT NULL,
    status TEXT NOT NULL,
    idempotency TEXT NOT NULL DEFAULT 'safe_to_retry',
    args JSONB,
    result JSONB,
    error JSONB,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (execution_id, kind, target, args_hash, occurrence)
);
CREATE INDEX IF NOT EXISTS steps_execution_idx ON steps (execution_id);

CREATE TABLE IF NOT EXISTS approvals (
    id UUID PRIMARY KEY,
    step_id TEXT NOT NULL REFERENCES steps(step_id) ON DELETE CASCADE,
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    approvers JSONB,
    message TEXT,
    timeout_at TIMESTAMPTZ NOT NULL,
    decided_by TEXT,
    decided_at TIMESTAMPTZ,
    rationale TEXT,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS approvals_pending_timeout_idx ON approvals (status, timeout_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS approvals_pending_execution_idx ON approvals (execution_id) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS dispatches (
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
CREATE INDEX IF NOT EXISTS dispatches_due_idx ON dispatches (status, next_attempt_at) WHERE status IN ('pending','failed');

CREATE TABLE IF NOT EXISTS rate_buckets (
    key            TEXT PRIMARY KEY,
    tokens         DOUBLE PRECISION NOT NULL,
    max_tokens     INTEGER NOT NULL,
    window_seconds DOUBLE PRECISION NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS rate_buckets_updated_idx ON rate_buckets (updated_at);

ALTER TABLE IF EXISTS executions ALTER COLUMN input  SET COMPRESSION lz4;
ALTER TABLE IF EXISTS executions ALTER COLUMN output SET COMPRESSION lz4;
ALTER TABLE IF EXISTS steps      ALTER COLUMN args   SET COMPRESSION lz4;
ALTER TABLE IF EXISTS steps      ALTER COLUMN result SET COMPRESSION lz4;
ALTER TABLE IF EXISTS steps      ALTER COLUMN error  SET COMPRESSION lz4;
