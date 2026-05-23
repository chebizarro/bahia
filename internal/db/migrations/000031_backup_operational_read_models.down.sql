DROP INDEX IF EXISTS idx_backup_retention_runs_failure_category;
DROP INDEX IF EXISTS idx_backup_restores_failure_category;
DROP INDEX IF EXISTS idx_backup_restores_approval_queue;
DROP INDEX IF EXISTS idx_backup_runs_failure_category;
DROP INDEX IF EXISTS idx_backup_runs_restore_eligibility;

ALTER TABLE backup_retention_runs
  DROP COLUMN IF EXISTS failure_category;

ALTER TABLE backup_restores
  DROP COLUMN IF EXISTS failure_category,
  DROP COLUMN IF EXISTS verification_policy_failure,
  DROP COLUMN IF EXISTS approval_reason,
  DROP COLUMN IF EXISTS approval_reason_code,
  DROP COLUMN IF EXISTS approval_requirement,
  DROP COLUMN IF EXISTS approval_required;

ALTER TABLE backup_verifications
  DROP COLUMN IF EXISTS evidence_details;

ALTER TABLE backup_runs
  DROP COLUMN IF EXISTS failure_category,
  DROP COLUMN IF EXISTS verification_policy_failure,
  DROP COLUMN IF EXISTS restore_eligibility_reason,
  DROP COLUMN IF EXISTS restore_eligibility,
  DROP COLUMN IF EXISTS verification_mode;
