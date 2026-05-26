-- 000037_desired_state_persistence: Add JSONB/hash columns for desired-state persistence
-- Adds desired_state snapshots and hash columns for cheap comparison/drift detection.
-- Secret plaintext is NEVER stored in these JSONB columns.

-- deployment_intents: store the desired-state snapshot and its hash at intent creation time
ALTER TABLE deployment_intents
    ADD COLUMN desired_state JSONB,
    ADD COLUMN desired_hash TEXT;

CREATE INDEX idx_deployment_intents_desired_hash ON deployment_intents(desired_hash);

-- deployment_runs: store runtime apply metadata (renderer info, apply results, etc.)
ALTER TABLE deployment_runs
    ADD COLUMN apply_metadata JSONB;

-- environment_service_state: store the latest desired runtime state and its hash
ALTER TABLE environment_service_state
    ADD COLUMN desired_runtime_state JSONB,
    ADD COLUMN desired_hash TEXT;

CREATE INDEX idx_environment_service_state_desired_hash ON environment_service_state(desired_hash);

-- runtime_observations: store normalized observed state and its hash for drift comparison
ALTER TABLE runtime_observations
    ADD COLUMN normalized_state JSONB,
    ADD COLUMN normalized_hash TEXT;

CREATE INDEX idx_runtime_observations_normalized_hash ON runtime_observations(normalized_hash);
