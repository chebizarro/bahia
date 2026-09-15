DROP INDEX IF EXISTS deployment_intents_service_runtime_release_uq;

DELETE FROM deployment_intents WHERE agent_runtime_release_id IS NOT NULL;

ALTER TABLE deployment_intents
    DROP CONSTRAINT IF EXISTS deployment_intents_exactly_one_artifact_source_ck,
    DROP COLUMN IF EXISTS agent_runtime_release_id,
    ALTER COLUMN artifact_id SET NOT NULL;
