ALTER TABLE environments DROP CONSTRAINT IF EXISTS fk_environments_org_id;
ALTER TABLE services DROP CONSTRAINT IF EXISTS fk_services_org_id;

UPDATE environments SET org_id = NULL WHERE org_id IS NOT NULL;
UPDATE services SET org_id = NULL WHERE org_id IS NOT NULL;

DROP INDEX IF EXISTS idx_org_invites_expires_at;
DROP INDEX IF EXISTS idx_org_invites_pubkey;
DROP INDEX IF EXISTS idx_org_invites_org_id;
DROP TABLE IF EXISTS org_invites;

DROP INDEX IF EXISTS idx_org_members_pubkey;
DROP TABLE IF EXISTS org_members;

DROP TABLE IF EXISTS organizations;
