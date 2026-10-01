-- An SBOM manifest whose reference event is queued in the publish outbox
-- (below the publish quorum, still being retried) is 'pending' until the
-- outbox delivers the reference (published) or abandons it (failed)
-- (bahia-irsry.40). sbom_manifests is small, so the widened check is added
-- and validated in one statement.
ALTER TABLE sbom_manifests
    DROP CONSTRAINT IF EXISTS sbom_manifests_publish_state_check,
    ADD CONSTRAINT sbom_manifests_publish_state_check
        CHECK (publish_state IN ('draft', 'pending', 'published', 'failed'));
