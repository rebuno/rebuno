-- +goose Up
ALTER TABLE executions DROP COLUMN agent_version;
