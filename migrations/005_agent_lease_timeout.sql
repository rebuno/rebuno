-- +goose Up
ALTER TABLE agents ADD COLUMN lease_timeout_seconds DOUBLE PRECISION;
