-- A store-backed secret has no PostgreSQL version row: its value lives only in
-- the secret store (docs/architecture/secret-store.md), so its access audit
-- names the credential version and no secret_versions row.
ALTER TABLE secret_access_audit ALTER COLUMN secret_version_id DROP NOT NULL;
