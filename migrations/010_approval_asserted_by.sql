-- +goose Up

ALTER TABLE approvals ADD COLUMN asserted_by TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE approvals DROP COLUMN asserted_by;
