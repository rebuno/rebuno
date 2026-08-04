-- Partial index for the stalled-step re-dispatch worker: it scans for steps in
-- the executing state whose started_at is older than the lease cutoff. Keeping
-- the predicate on status = 'executing' makes the index small (only in-flight
-- steps) and cheap to maintain.
CREATE INDEX IF NOT EXISTS steps_stalled_idx
    ON steps (started_at)
    WHERE status = 'executing';
