ALTER TABLE managed_instance_recovery_attempts
  DROP CONSTRAINT IF EXISTS managed_instance_recovery_attempts_result_check;
ALTER TABLE managed_instance_recovery_attempts
  ADD CONSTRAINT managed_instance_recovery_attempts_result_check
  CHECK (result IN ('pending', 'success', 'degraded', 'failed', 'budget_exhausted', 'skipped_override'));
