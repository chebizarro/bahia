-- Reverse 000038_deployment_units.

DROP INDEX IF EXISTS idx_environment_service_state_deployment_unit_id;
ALTER TABLE environment_service_state
  DROP COLUMN IF EXISTS deployment_unit_id;

DROP INDEX IF EXISTS idx_runtime_observations_deployment_unit_id;
ALTER TABLE runtime_observations
  DROP COLUMN IF EXISTS deployment_unit_id;

DROP INDEX IF EXISTS idx_deployment_runs_deployment_unit_id;
ALTER TABLE deployment_runs
  DROP COLUMN IF EXISTS deployment_unit_id;

DROP INDEX IF EXISTS idx_deployment_intents_deployment_unit_id;
ALTER TABLE deployment_intents
  DROP COLUMN IF EXISTS deployment_unit_id;

DROP INDEX IF EXISTS idx_deployment_units_runtime_type;
DROP INDEX IF EXISTS idx_deployment_units_environment_id;
DROP TABLE IF EXISTS deployment_units;
