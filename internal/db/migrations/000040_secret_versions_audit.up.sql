CREATE TABLE IF NOT EXISTS secret_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    secret_id UUID NOT NULL REFERENCES service_secrets(id) ON DELETE CASCADE,
    version INT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    encryption_method VARCHAR(20) NOT NULL,
    created_by VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (secret_id, version)
);

INSERT INTO secret_versions (secret_id, version, encrypted_value, encryption_method, created_by, created_at)
SELECT id, 1, encrypted_value, encryption_method, created_by, created_at
FROM service_secrets
ON CONFLICT (secret_id, version) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_secret_versions_secret ON secret_versions(secret_id, version DESC);

CREATE TABLE IF NOT EXISTS secret_access_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    secret_id UUID NOT NULL REFERENCES service_secrets(id) ON DELETE CASCADE,
    secret_version_id UUID NOT NULL REFERENCES secret_versions(id) ON DELETE RESTRICT,
    version INT NOT NULL,
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID REFERENCES environments(id) ON DELETE SET NULL,
    operation VARCHAR(64) NOT NULL,
    outcome VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    request_id VARCHAR(255) NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (outcome IN ('success', 'failure')),
    CHECK (operation IN ('resolve', 'runtime_apply'))
);

CREATE INDEX IF NOT EXISTS idx_secret_access_audit_secret ON secret_access_audit(secret_id, accessed_at DESC);
CREATE INDEX IF NOT EXISTS idx_secret_access_audit_service_env ON secret_access_audit(service_id, environment_id, accessed_at DESC);
