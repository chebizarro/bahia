ALTER TABLE adopted_runtime_identity
    DROP CONSTRAINT IF EXISTS adopted_runtime_identity_fingerprint_key;

ALTER TABLE adopted_runtime_identity
    ADD CONSTRAINT adopted_runtime_identity_org_fingerprint_key UNIQUE (org_id, fingerprint);
