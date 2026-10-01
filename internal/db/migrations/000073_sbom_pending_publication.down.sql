-- Before 000073 a queued reference was recorded as published, so pending
-- manifests go back to that state.
UPDATE sbom_manifests SET publish_state = 'published' WHERE publish_state = 'pending';
ALTER TABLE sbom_manifests
    DROP CONSTRAINT IF EXISTS sbom_manifests_publish_state_check,
    ADD CONSTRAINT sbom_manifests_publish_state_check
        CHECK (publish_state IN ('draft', 'published', 'failed'));
