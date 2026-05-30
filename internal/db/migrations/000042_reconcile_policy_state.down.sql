DROP INDEX IF EXISTS idx_environment_service_state_reconcile_backoff;

ALTER TABLE environment_service_state
  DROP COLUMN IF EXISTS reconcile_consecutive_failures,
  DROP COLUMN IF EXISTS reconcile_backoff_until,
  DROP COLUMN IF EXISTS reconcile_failure_metadata;
