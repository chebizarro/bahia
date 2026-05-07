-- Extend package lifecycle projection vocabulary for signer-first Item 3.

ALTER TABLE package_publications_projection
    DROP CONSTRAINT IF EXISTS package_publications_projection_status_check;
ALTER TABLE package_publications_projection
    ADD CONSTRAINT package_publications_projection_status_check
    CHECK (status IN ('pending', 'approved', 'running', 'succeeded', 'published', 'promoting', 'promoted', 'rejected', 'rolled_back', 'failed'));

ALTER TABLE package_intents_projection
    DROP CONSTRAINT IF EXISTS package_intents_projection_operation_check;
ALTER TABLE package_intents_projection
    ADD CONSTRAINT package_intents_projection_operation_check
    CHECK (operation IN ('repository_apply', 'repository_delete', 'artifact_publish', 'artifact_delete', 'promote', 'yank', 'deprecate', 'drift_detect'));
