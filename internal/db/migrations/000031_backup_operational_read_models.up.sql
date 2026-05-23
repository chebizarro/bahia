-- First-class backup operational state for verification, restore approval, and observability.
ALTER TABLE backup_runs
  ADD COLUMN verification_mode TEXT NOT NULL DEFAULT 'none',
  ADD COLUMN restore_eligibility TEXT NOT NULL DEFAULT 'unknown',
  ADD COLUMN restore_eligibility_reason TEXT NOT NULL DEFAULT '',
  ADD COLUMN verification_policy_failure TEXT NOT NULL DEFAULT '',
  ADD COLUMN failure_category TEXT NOT NULL DEFAULT '';

UPDATE backup_runs
SET verification_mode = COALESCE(NULLIF((metadata->>'effective_verification_mode'), ''), 'none'),
    restore_eligibility = CASE
      WHEN status = 'succeeded' AND snapshot_created AND snapshot_id <> '' AND verification_status = 'succeeded' THEN 'eligible'
      WHEN status <> 'succeeded' THEN 'run_not_succeeded'
      WHEN NOT snapshot_created OR snapshot_id = '' THEN 'snapshot_missing'
      WHEN verification_status = 'pending' THEN 'verification_pending'
      WHEN verification_status = 'failed' THEN 'verification_failed'
      WHEN verification_status = 'skipped' THEN 'verification_skipped'
      WHEN verification_status = 'unsupported' THEN 'verification_unsupported'
      ELSE 'unknown'
    END,
    restore_eligibility_reason = CASE
      WHEN status = 'succeeded' AND snapshot_created AND snapshot_id <> '' AND verification_status = 'succeeded' THEN 'backup snapshot verified successfully'
      WHEN status <> 'succeeded' THEN 'backup run has not succeeded'
      WHEN NOT snapshot_created OR snapshot_id = '' THEN 'backup snapshot is missing'
      WHEN verification_status = 'pending' THEN 'backup snapshot verification is pending'
      WHEN verification_status = 'failed' THEN 'backup snapshot verification failed'
      WHEN verification_status = 'skipped' THEN 'backup snapshot verification was skipped'
      WHEN verification_status = 'unsupported' THEN 'backup snapshot verification is unsupported'
      ELSE 'backup snapshot verification state is unknown'
    END,
    verification_policy_failure = CASE
      WHEN snapshot_created AND snapshot_id <> '' AND verification_status <> 'succeeded' AND error <> '' THEN error
      ELSE verification_policy_failure
    END,
    failure_category = CASE
      WHEN status = 'failed' AND error <> '' THEN 'unknown'
      ELSE failure_category
    END;

ALTER TABLE backup_verifications
  ADD COLUMN evidence_details JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE backup_verifications
SET evidence_details = evidence
WHERE evidence_details = '{}'::jsonb AND evidence <> '{}'::jsonb;

ALTER TABLE backup_restores
  ADD COLUMN approval_required BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN approval_requirement TEXT NOT NULL DEFAULT 'policy',
  ADD COLUMN approval_reason_code TEXT NOT NULL DEFAULT '',
  ADD COLUMN approval_reason JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN verification_policy_failure TEXT NOT NULL DEFAULT '',
  ADD COLUMN failure_category TEXT NOT NULL DEFAULT '';

UPDATE backup_restores
SET approval_required = approval_status <> 'not_required',
    approval_requirement = CASE WHEN approval_status = 'not_required' THEN 'none' ELSE 'policy' END,
    approval_reason_code = CASE
      WHEN approval_message <> '' AND approval_status = 'approved' THEN 'operator_approved'
      WHEN approval_message <> '' AND approval_status = 'rejected' THEN 'operator_rejected'
      ELSE approval_reason_code
    END,
    approval_reason = CASE
      WHEN approval_message <> '' THEN jsonb_build_object('message', approval_message)
      ELSE approval_reason
    END,
    failure_category = CASE
      WHEN approval_status = 'rejected' THEN 'approval_rejected'
      WHEN status = 'failed' AND error <> '' THEN 'unknown'
      ELSE failure_category
    END;

ALTER TABLE backup_retention_runs
  ADD COLUMN failure_category TEXT NOT NULL DEFAULT '';

UPDATE backup_retention_runs
SET failure_category = CASE
  WHEN status = 'failed' AND error <> '' THEN 'unknown'
  ELSE failure_category
END;

CREATE INDEX idx_backup_runs_restore_eligibility ON backup_runs(restore_eligibility);
CREATE INDEX idx_backup_runs_failure_category ON backup_runs(failure_category);
CREATE INDEX idx_backup_restores_approval_queue ON backup_restores(approval_status, approval_required);
CREATE INDEX idx_backup_restores_failure_category ON backup_restores(failure_category);
CREATE INDEX idx_backup_retention_runs_failure_category ON backup_retention_runs(failure_category);
