ALTER TABLE executions ADD COLUMN labels JSONB NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_executions_labels ON executions USING GIN (labels);
