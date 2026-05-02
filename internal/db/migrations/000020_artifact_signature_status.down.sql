DROP INDEX IF EXISTS idx_artifact_signatures_artifact_verified;
DROP INDEX IF EXISTS idx_artifact_signatures_verification_status;

ALTER TABLE artifact_signatures
    DROP COLUMN IF EXISTS verification_status;
