-- +goose Up

ALTER TABLE steps ADD COLUMN resources TEXT[];

CREATE TABLE execution_resources (
    execution_id UUID NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    driver_id TEXT NOT NULL,
    config JSONB,
    coverage_reuse BOOLEAN NOT NULL,
    every_steps INT NOT NULL CHECK (every_steps > 0),
    on_completion BOOLEAN NOT NULL,
    registered_seq BIGINT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    count INT NOT NULL DEFAULT 0,
    binding JSONB,
    checkpoint_ref TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (execution_id, key)
);

CREATE TABLE resource_checkpoints (
    execution_id UUID NOT NULL,
    key TEXT NOT NULL,
    covered_seq BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    checkpoint_ref TEXT NOT NULL,
    invalidated_seq BIGINT,
    PRIMARY KEY (execution_id, key, covered_seq),
    FOREIGN KEY (execution_id, key) REFERENCES execution_resources (execution_id, key) ON DELETE CASCADE
);

-- +goose Down

DROP TABLE resource_checkpoints;
DROP TABLE execution_resources;
ALTER TABLE steps DROP COLUMN resources;
