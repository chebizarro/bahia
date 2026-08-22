ALTER TABLE hiveci_accepted_releases
    ADD COLUMN policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN workflow_run_signed_event JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN worker_admission_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN rollback_compatibility JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN health_readiness_contracts JSONB NOT NULL DEFAULT '{}'::jsonb;
