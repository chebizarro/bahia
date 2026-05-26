-- Reverse 000037_desired_state_persistence

DROP INDEX IF EXISTS idx_runtime_observations_normalized_hash;
ALTER TABLE runtime_observations
    DROP COLUMN IF EXISTS normalized_state,
    DROP COLUMN IF EXISTS normalized_hash;

DROP INDEX IF EXISTS idx_environment_service_state_desired_hash;
ALTER TABLE environment_service_state
    DROP COLUMN IF EXISTS desired_runtime_state,
    DROP COLUMN IF EXISTS desired_hash;

ALTER TABLE deployment_runs
    DROP COLUMN IF EXISTS apply_metadata;

DROP INDEX IF EXISTS idx_deployment_intents_desired_hash;
ALTER TABLE deployment_intents
    DROP COLUMN IF EXISTS desired_state,
    DROP COLUMN IF EXISTS desired_hash;
