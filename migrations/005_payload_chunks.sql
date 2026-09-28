-- +goose Up

CREATE TABLE payload_chunks (
    hash BYTEA PRIMARY KEY CHECK (octet_length(hash) = 32),
    data BYTEA COMPRESSION lz4 NOT NULL,
    used_at TIMESTAMPTZ NOT NULL
) WITH (toast_tuple_target = 128);

ALTER TABLE steps ADD COLUMN args_chunks BYTEA[];
ALTER TABLE executions ADD COLUMN state_chunks BYTEA[];

CREATE INDEX steps_args_chunks_idx ON steps USING GIN (args_chunks);
CREATE INDEX executions_state_chunks_idx ON executions USING GIN (state_chunks);

-- +goose Down

DROP INDEX executions_state_chunks_idx;
DROP INDEX steps_args_chunks_idx;
ALTER TABLE executions DROP COLUMN state_chunks;
ALTER TABLE steps DROP COLUMN args_chunks;
DROP TABLE payload_chunks;
