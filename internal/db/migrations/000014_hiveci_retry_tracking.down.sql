-- 000014_hiveci_retry_tracking.down.sql

DROP INDEX IF EXISTS idx_hiveci_workflow_results_retry;

ALTER TABLE hiveci_workflow_results
  DROP COLUMN IF EXISTS last_retry_at,
  DROP COLUMN IF EXISTS retry_count;
