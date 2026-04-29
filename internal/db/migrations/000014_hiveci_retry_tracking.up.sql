-- 000014_hiveci_retry_tracking.up.sql
-- Adds retry tracking columns for Hive-CI background retry processing.

ALTER TABLE hiveci_workflow_results
  ADD COLUMN IF NOT EXISTS retry_count INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS last_retry_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_hiveci_workflow_results_retry
  ON hiveci_workflow_results(processing_state, retry_count, last_retry_at);
