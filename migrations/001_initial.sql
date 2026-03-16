CREATE TABLE IF NOT EXISTS executions (
  id            TEXT PRIMARY KEY,
  agent_id      TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending',
  next_sequence BIGINT NOT NULL DEFAULT 1,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status);
CREATE INDEX IF NOT EXISTS idx_executions_status_updated ON executions(status, updated_at)
  WHERE status IN ('completed', 'failed', 'cancelled');
CREATE INDEX IF NOT EXISTS idx_executions_agent ON executions(agent_id);
CREATE INDEX IF NOT EXISTS idx_executions_created_id ON executions(created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS events (
  id              UUID PRIMARY KEY,
  execution_id    TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
  step_id         TEXT,
  type            TEXT NOT NULL,
  schema_version  INTEGER NOT NULL DEFAULT 1,
  payload         JSONB,
  timestamp       TIMESTAMPTZ NOT NULL,
  sequence        BIGINT NOT NULL,
  idempotency_key TEXT,
  causation_id    UUID NOT NULL,
  correlation_id  UUID NOT NULL,
  UNIQUE (execution_id, sequence),
  UNIQUE (idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_events_exec_type ON events(execution_id, type);

CREATE TABLE IF NOT EXISTS checkpoints (
  execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
  sequence     BIGINT NOT NULL,
  state_data   JSONB NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS signals (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
  signal_type  TEXT NOT NULL,
  payload      JSONB,
  consumed     BOOLEAN NOT NULL DEFAULT FALSE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_signals_exec ON signals(execution_id)
  WHERE consumed = FALSE;
CREATE INDEX IF NOT EXISTS idx_signals_exec_type ON signals(execution_id, signal_type)
  WHERE consumed = FALSE;

CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,
  execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
  agent_id     TEXT NOT NULL,
  consumer_id  TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_execution ON sessions(execution_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS runners (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL DEFAULT '',
  capabilities   JSONB,
  status         TEXT NOT NULL DEFAULT 'online',
  last_heartbeat TIMESTAMPTZ NOT NULL,
  registered_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  metadata       JSONB
);
CREATE INDEX IF NOT EXISTS idx_runners_heartbeat ON runners(last_heartbeat)
  WHERE status = 'online';
