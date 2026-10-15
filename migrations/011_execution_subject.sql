-- +goose Up

ALTER TABLE executions
    ADD COLUMN created_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN on_behalf_of TEXT NOT NULL DEFAULT '',
    ADD COLUMN subject_groups TEXT[];

-- +goose Down

ALTER TABLE executions DROP COLUMN subject_groups;
ALTER TABLE executions DROP COLUMN on_behalf_of;
ALTER TABLE executions DROP COLUMN created_by;
