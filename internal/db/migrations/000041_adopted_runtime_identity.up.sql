CREATE TABLE IF NOT EXISTS org_ownership_repair (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type TEXT NOT NULL CHECK (resource_type IN ('service', 'environment')),
    resource_id UUID NOT NULL,
    resource_name TEXT NOT NULL,
    repair_reason TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'needs_operator_repair' CHECK (status IN ('needs_operator_repair', 'repaired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (resource_type, resource_id)
);

WITH org_count AS (
    SELECT count(*) AS n FROM organizations
)
INSERT INTO org_ownership_repair (resource_type, resource_id, resource_name, repair_reason)
SELECT 'service', s.id, s.name, 'service org_id is unresolved and cannot be inferred because organization count is not exactly one'
FROM services s, org_count
WHERE s.org_id IS NULL AND org_count.n <> 1
ON CONFLICT (resource_type, resource_id) DO UPDATE SET
    repair_reason = EXCLUDED.repair_reason,
    status = 'needs_operator_repair',
    updated_at = now();

WITH org_count AS (
    SELECT count(*) AS n FROM organizations
)
INSERT INTO org_ownership_repair (resource_type, resource_id, resource_name, repair_reason)
SELECT 'environment', e.id, e.name, 'environment org_id is unresolved and cannot be inferred because organization count is not exactly one'
FROM environments e, org_count
WHERE e.org_id IS NULL AND org_count.n <> 1
ON CONFLICT (resource_type, resource_id) DO UPDATE SET
    repair_reason = EXCLUDED.repair_reason,
    status = 'needs_operator_repair',
    updated_at = now();

CREATE INDEX IF NOT EXISTS idx_org_ownership_repair_status ON org_ownership_repair(status, resource_type);

CREATE TABLE IF NOT EXISTS adopted_runtime_identity (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    fingerprint_kind TEXT NOT NULL,
    fingerprint TEXT NOT NULL UNIQUE,
    container_id TEXT NOT NULL DEFAULT '',
    image_digest TEXT NOT NULL DEFAULT '',
    endpoint_ref TEXT NOT NULL DEFAULT '',
    host_alias TEXT NOT NULL DEFAULT '',
    target_name TEXT NOT NULL DEFAULT '',
    compose JSONB NOT NULL DEFAULT 'null'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (fingerprint_kind IN ('container_id', 'image_digest', 'compose_coordinates', 'endpoint_target')),
    CHECK (fingerprint <> '')
);

CREATE INDEX IF NOT EXISTS idx_adopted_runtime_identity_service_env ON adopted_runtime_identity(service_id, environment_id);
CREATE INDEX IF NOT EXISTS idx_adopted_runtime_identity_org ON adopted_runtime_identity(org_id);
CREATE INDEX IF NOT EXISTS idx_adopted_runtime_identity_container ON adopted_runtime_identity(container_id) WHERE container_id <> '';
CREATE INDEX IF NOT EXISTS idx_adopted_runtime_identity_digest ON adopted_runtime_identity(image_digest) WHERE image_digest <> '';
