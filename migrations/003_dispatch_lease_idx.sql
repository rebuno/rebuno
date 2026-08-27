-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY dispatches_lease_idx ON dispatches (locked_at) WHERE status = 'in_flight';
