-- Allow deployment intents to reference one shared verified agent runtime
-- release directly, without fabricating a service-scoped build/artifact row.
ALTER TABLE deployment_intents
    ALTER COLUMN artifact_id DROP NOT NULL,
    ADD COLUMN agent_runtime_release_id UUID REFERENCES agent_runtime_releases(id) ON DELETE RESTRICT,
    ADD CONSTRAINT deployment_intents_exactly_one_artifact_source_ck
        CHECK (num_nonnulls(artifact_id, agent_runtime_release_id) = 1);

CREATE UNIQUE INDEX deployment_intents_service_runtime_release_uq
    ON deployment_intents(service_id, agent_runtime_release_id)
    WHERE agent_runtime_release_id IS NOT NULL;
