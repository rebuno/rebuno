-- +goose Up

ALTER TABLE executions
    ADD COLUMN forked_from UUID REFERENCES executions(id) ON DELETE SET NULL,
    ADD COLUMN fork_seq BIGINT,
    ADD COLUMN policy_bundle TEXT;

CREATE INDEX executions_forked_from_idx ON executions (forked_from) WHERE forked_from IS NOT NULL;

-- +goose Down

DROP INDEX executions_forked_from_idx;
ALTER TABLE executions DROP COLUMN policy_bundle;
ALTER TABLE executions DROP COLUMN fork_seq;
ALTER TABLE executions DROP COLUMN forked_from;
