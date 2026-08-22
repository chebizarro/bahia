ALTER TABLE hiveci_accepted_releases
    DROP COLUMN IF EXISTS health_readiness_contracts,
    DROP COLUMN IF EXISTS rollback_compatibility,
    DROP COLUMN IF EXISTS worker_admission_evidence,
    DROP COLUMN IF EXISTS workflow_run_signed_event,
    DROP COLUMN IF EXISTS policy_snapshot;
