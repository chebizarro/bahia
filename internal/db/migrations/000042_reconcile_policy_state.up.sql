ALTER TABLE environment_service_state
  ADD COLUMN IF NOT EXISTS reconcile_failure_metadata JSONB,
  ADD COLUMN IF NOT EXISTS reconcile_backoff_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS reconcile_consecutive_failures INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_environment_service_state_reconcile_backoff
  ON environment_service_state(reconcile_backoff_until)
  WHERE reconcile_backoff_until IS NOT NULL;
