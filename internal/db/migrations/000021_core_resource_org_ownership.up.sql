-- Attach core service/environment resources to tenant organizations.
-- Existing rows are backfilled only when there is exactly one organization;
-- otherwise they remain unowned and cannot pass tenant RBAC until assigned.
ALTER TABLE services ADD COLUMN IF NOT EXISTS org_id UUID;
ALTER TABLE environments ADD COLUMN IF NOT EXISTS org_id UUID;

UPDATE services SET org_id = NULL WHERE org_id = '00000000-0000-0000-0000-000000000000'::uuid;
UPDATE environments SET org_id = NULL WHERE org_id = '00000000-0000-0000-0000-000000000000'::uuid;

WITH single_org AS (
    SELECT id FROM organizations
    ORDER BY created_at
    LIMIT 1
), org_count AS (
    SELECT count(*) AS n FROM organizations
)
UPDATE services
SET org_id = (SELECT id FROM single_org)
WHERE org_id IS NULL AND (SELECT n FROM org_count) = 1;

WITH single_org AS (
    SELECT id FROM organizations
    ORDER BY created_at
    LIMIT 1
), org_count AS (
    SELECT count(*) AS n FROM organizations
)
UPDATE environments
SET org_id = (SELECT id FROM single_org)
WHERE org_id IS NULL AND (SELECT n FROM org_count) = 1;

CREATE INDEX IF NOT EXISTS idx_services_org_id ON services(org_id);
CREATE INDEX IF NOT EXISTS idx_environments_org_id ON environments(org_id);
