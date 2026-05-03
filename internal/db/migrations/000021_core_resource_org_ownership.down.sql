DROP INDEX IF EXISTS idx_environments_org_id;
DROP INDEX IF EXISTS idx_services_org_id;
ALTER TABLE environments DROP COLUMN IF EXISTS org_id;
ALTER TABLE services DROP COLUMN IF EXISTS org_id;
