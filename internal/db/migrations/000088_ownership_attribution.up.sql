-- These rows are disposable indexes of signed cp-state, never grants.
CREATE TABLE security_attributions (
    org_id UUID NOT NULL,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('security-target', 'sbom')),
    subject_digest TEXT NOT NULL CHECK (subject_digest ~ '^[0-9a-f]{64}$'),
    visible BOOLEAN NOT NULL,
    source_event_id TEXT NOT NULL CHECK (source_event_id ~ '^[0-9a-f]{64}$'),
    source_created_at BIGINT NOT NULL,
    PRIMARY KEY (org_id, subject_type, subject_digest)
);
CREATE INDEX security_attributions_visible_org ON security_attributions (org_id, subject_type) WHERE visible;

ALTER TABLE backup_repositories ADD COLUMN org_id UUID;
ALTER TABLE backup_policies ADD COLUMN org_id UUID;
ALTER TABLE backup_recipes ADD COLUMN org_id UUID;
ALTER TABLE backup_definitions ADD COLUMN org_id UUID;
ALTER TABLE backup_repositories ADD COLUMN canonical_deleted BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE backup_policies ADD COLUMN canonical_deleted BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE backup_recipes ADD COLUMN canonical_deleted BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE backup_definitions ADD COLUMN canonical_deleted BOOLEAN NOT NULL DEFAULT false;
-- Existing SQL rows have no canonical ownership proof. They remain only as
-- foreign-key history until a verified signed head replays over each row.
UPDATE backup_repositories SET canonical_deleted = true;
UPDATE backup_policies SET canonical_deleted = true;
UPDATE backup_recipes SET canonical_deleted = true;
UPDATE backup_definitions SET canonical_deleted = true;
ALTER TABLE backup_repositories ADD CONSTRAINT backup_repositories_canonical_owner CHECK (canonical_deleted OR org_id IS NOT NULL);
ALTER TABLE backup_policies ADD CONSTRAINT backup_policies_canonical_owner CHECK (canonical_deleted OR org_id IS NOT NULL);
ALTER TABLE backup_recipes ADD CONSTRAINT backup_recipes_canonical_owner CHECK (canonical_deleted OR org_id IS NOT NULL);
ALTER TABLE backup_definitions ADD CONSTRAINT backup_definitions_canonical_owner CHECK (canonical_deleted OR org_id IS NOT NULL);
ALTER TABLE backup_repositories DROP CONSTRAINT backup_repositories_name_key;
ALTER TABLE backup_policies DROP CONSTRAINT backup_policies_name_key;
ALTER TABLE backup_recipes DROP CONSTRAINT backup_recipes_name_version_key;
ALTER TABLE backup_definitions DROP CONSTRAINT backup_definitions_name_key;
CREATE UNIQUE INDEX backup_repositories_org_name ON backup_repositories (org_id, name) WHERE org_id IS NOT NULL AND NOT canonical_deleted;
CREATE UNIQUE INDEX backup_policies_org_name ON backup_policies (org_id, name) WHERE org_id IS NOT NULL AND NOT canonical_deleted;
CREATE UNIQUE INDEX backup_recipes_org_name_version ON backup_recipes (org_id, name, version) WHERE org_id IS NOT NULL AND NOT canonical_deleted;
CREATE UNIQUE INDEX backup_definitions_org_name ON backup_definitions (org_id, name) WHERE org_id IS NOT NULL AND NOT canonical_deleted;
CREATE INDEX backup_repositories_org_id ON backup_repositories (org_id);
CREATE INDEX backup_policies_org_id ON backup_policies (org_id);
CREATE INDEX backup_recipes_org_id ON backup_recipes (org_id);
CREATE INDEX backup_definitions_org_id ON backup_definitions (org_id);
