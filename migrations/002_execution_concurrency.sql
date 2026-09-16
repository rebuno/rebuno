-- +goose Up

ALTER TABLE executions ADD COLUMN concurrency_key TEXT;
ALTER TABLE executions ADD CONSTRAINT executions_concurrency_key_valid
    CHECK (concurrency_key IS NULL OR octet_length(concurrency_key) BETWEEN 1 AND 256);

CREATE UNIQUE INDEX executions_one_active_per_key
    ON executions (concurrency_key)
    WHERE concurrency_key IS NOT NULL AND status IN ('running', 'blocked');
CREATE INDEX executions_pending_key_idx
    ON executions (concurrency_key, id)
    WHERE concurrency_key IS NOT NULL AND status = 'pending';

-- +goose Down

DROP INDEX executions_pending_key_idx;
DROP INDEX executions_one_active_per_key;
ALTER TABLE executions DROP CONSTRAINT executions_concurrency_key_valid;
ALTER TABLE executions DROP COLUMN concurrency_key;
