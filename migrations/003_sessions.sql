-- +goose Up

ALTER TABLE executions RENAME COLUMN concurrency_key TO session;
ALTER TABLE executions RENAME CONSTRAINT executions_concurrency_key_valid TO executions_session_valid;
ALTER INDEX executions_one_active_per_key RENAME TO executions_one_active_per_session;
ALTER INDEX executions_pending_key_idx RENAME TO executions_pending_session_idx;

ALTER TABLE executions
    ADD COLUMN parent_execution_id UUID REFERENCES executions(id) ON DELETE SET NULL,
    ADD COLUMN state JSONB COMPRESSION lz4;

CREATE INDEX executions_session_idx ON executions (session, id) WHERE session IS NOT NULL;
CREATE INDEX executions_parent_idx ON executions (parent_execution_id) WHERE parent_execution_id IS NOT NULL;

-- +goose Down

DROP INDEX executions_parent_idx;
DROP INDEX executions_session_idx;
ALTER TABLE executions DROP COLUMN state;
ALTER TABLE executions DROP COLUMN parent_execution_id;
ALTER INDEX executions_pending_session_idx RENAME TO executions_pending_key_idx;
ALTER INDEX executions_one_active_per_session RENAME TO executions_one_active_per_key;
ALTER TABLE executions RENAME CONSTRAINT executions_session_valid TO executions_concurrency_key_valid;
ALTER TABLE executions RENAME COLUMN session TO concurrency_key;
