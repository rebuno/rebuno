-- +goose Up

ALTER TABLE executions
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN spawned_by_execution_id UUID REFERENCES executions(id) ON DELETE SET NULL,
    ADD COLUMN spawned_by_step_id TEXT;

CREATE UNIQUE INDEX executions_idempotency_key_idx ON executions (agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX executions_spawned_by_step_idx ON executions (spawned_by_step_id) WHERE spawned_by_step_id IS NOT NULL;
CREATE INDEX executions_spawned_by_idx ON executions (spawned_by_execution_id, id) WHERE spawned_by_execution_id IS NOT NULL;

-- +goose Down

DROP INDEX executions_spawned_by_idx;
DROP INDEX executions_spawned_by_step_idx;
DROP INDEX executions_idempotency_key_idx;
ALTER TABLE executions DROP COLUMN spawned_by_step_id;
ALTER TABLE executions DROP COLUMN spawned_by_execution_id;
ALTER TABLE executions DROP COLUMN idempotency_key;
