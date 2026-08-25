-- +goose Up
ALTER TABLE steps ADD COLUMN usage_input INT;
ALTER TABLE steps ADD COLUMN usage_output INT;
