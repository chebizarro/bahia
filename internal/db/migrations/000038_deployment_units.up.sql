-- 000038_deployment_units: Add deployment unit persistence and nullable placements.
-- Deployment units are persisted only by explicit operator unit creation or first multi-unit config change.
-- Existing single-unit deployments continue to use an in-memory implicit default unit with NULL placement columns.

CREATE TABLE deployment_units (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  unit_key TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  runtime_type TEXT NOT NULL,
  reconcile_mode TEXT NOT NULL,
  ownership_mode TEXT NOT NULL,
  runtime_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (environment_id, unit_key),
  CHECK (unit_key <> ''),
  CHECK (runtime_type IN ('docker', 'compose', 'kubernetes', 'podman')),
  CHECK (reconcile_mode IN ('observe_only', 'auto_apply', 'approval_required', 'disabled')),
  CHECK (ownership_mode IN ('bahia_managed', 'adopted', 'external'))
);

CREATE INDEX idx_deployment_units_environment_id ON deployment_units(environment_id);
CREATE INDEX idx_deployment_units_runtime_type ON deployment_units(runtime_type);

ALTER TABLE deployment_intents
  ADD COLUMN deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE SET NULL;
CREATE INDEX idx_deployment_intents_deployment_unit_id ON deployment_intents(deployment_unit_id);

ALTER TABLE deployment_runs
  ADD COLUMN deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE SET NULL;
CREATE INDEX idx_deployment_runs_deployment_unit_id ON deployment_runs(deployment_unit_id);

ALTER TABLE runtime_observations
  ADD COLUMN deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE SET NULL;
CREATE INDEX idx_runtime_observations_deployment_unit_id ON runtime_observations(deployment_unit_id);

ALTER TABLE environment_service_state
  ADD COLUMN deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE SET NULL;
CREATE INDEX idx_environment_service_state_deployment_unit_id ON environment_service_state(deployment_unit_id);
