-- +goose Up

ALTER TABLE agents
    ADD COLUMN description TEXT NOT NULL DEFAULT '',
    ADD COLUMN input_schema JSONB,
    ADD COLUMN output_schema JSONB;

-- +goose Down

ALTER TABLE agents DROP COLUMN output_schema;
ALTER TABLE agents DROP COLUMN input_schema;
ALTER TABLE agents DROP COLUMN description;
