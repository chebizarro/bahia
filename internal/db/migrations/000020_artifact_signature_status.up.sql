-- Track discovered versus cryptographically verified artifact signatures.
ALTER TABLE artifact_signatures
    ADD COLUMN verification_status TEXT NOT NULL DEFAULT 'discovered'
        CHECK (verification_status IN ('verified', 'discovered', 'rejected', 'error'));

UPDATE artifact_signatures
SET verification_status = CASE
    WHEN verified = true THEN 'verified'
    WHEN verification_error IS NOT NULL AND verification_error <> '' THEN 'rejected'
    ELSE 'discovered'
END;

CREATE INDEX idx_artifact_signatures_verification_status
    ON artifact_signatures(verification_status);

CREATE INDEX idx_artifact_signatures_artifact_verified
    ON artifact_signatures(artifact_id)
    WHERE verification_status = 'verified';
