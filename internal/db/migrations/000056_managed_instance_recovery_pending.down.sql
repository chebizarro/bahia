UPDATE managed_instance_recovery_attempts SET result = 'failed', evidence = 'recovery result unresolved during rollback' WHERE result = 'pending';
ALTER TABLE managed_instance_recovery_attempts
  DROP CONSTRAINT IF EXISTS managed_instance_recovery_attempts_result_check;
ALTER TABLE managed_instance_recovery_attempts
  ADD CONSTRAINT managed_instance_recovery_attempts_result_check
  CHECK (result IN ('success', 'degraded', 'failed', 'budget_exhausted', 'skipped_override'));
