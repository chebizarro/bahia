-- First-class LLM control-plane schema.
CREATE TABLE llm_routes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  gateway_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  default_placement_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
  default_promotion_gate JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm_releases (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  route_id UUID NOT NULL REFERENCES llm_routes(id) ON DELETE CASCADE,
  version TEXT NOT NULL,
  model_ref TEXT NOT NULL,
  model_source TEXT NOT NULL,
  model_revision TEXT NOT NULL DEFAULT '',
  estimated_vram_gb INTEGER NOT NULL DEFAULT 0,
  backend_preferences JSONB NOT NULL DEFAULT '[]'::jsonb,
  runtime_backend JSONB NOT NULL DEFAULT '{}'::jsonb,
  external_backend JSONB NOT NULL DEFAULT '{}'::jsonb,
  placement_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
  promotion_gate JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (route_id, version),
  UNIQUE (route_id, id)
);

CREATE INDEX idx_llm_releases_route_id ON llm_releases(route_id);

CREATE TABLE llm_deployment_intents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  route_id UUID NOT NULL REFERENCES llm_routes(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  release_id UUID NOT NULL REFERENCES llm_releases(id) ON DELETE CASCADE,
  requested_by TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  approval_status TEXT NOT NULL DEFAULT 'not_required',
  status TEXT NOT NULL,
  supersedes_intent_id UUID REFERENCES llm_deployment_intents(id),
  approval_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  approved_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE llm_deployment_intents
  ADD CONSTRAINT fk_llm_intents_route_release
  FOREIGN KEY (route_id, release_id) REFERENCES llm_releases(route_id, id) ON DELETE CASCADE;

CREATE INDEX idx_llm_deployment_intents_route_env ON llm_deployment_intents(route_id, environment_id);
CREATE INDEX idx_llm_deployment_intents_status ON llm_deployment_intents(status);

CREATE TABLE llm_deployment_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_intent_id UUID NOT NULL REFERENCES llm_deployment_intents(id) ON DELETE CASCADE,
  backend_kind TEXT NOT NULL DEFAULT '',
  endpoint_ref TEXT NOT NULL DEFAULT '',
  worker_pubkey TEXT NOT NULL DEFAULT '',
  worker_name TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  exit_code INTEGER,
  stdout_ref TEXT NOT NULL DEFAULT '',
  stderr_ref TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_llm_deployment_runs_intent_id ON llm_deployment_runs(deployment_intent_id);
CREATE INDEX idx_llm_deployment_runs_status ON llm_deployment_runs(status);
CREATE UNIQUE INDEX idx_llm_deployment_runs_active_intent
  ON llm_deployment_runs(deployment_intent_id)
  WHERE status IN ('queued', 'running');

CREATE TABLE llm_route_observations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  route_id UUID NOT NULL REFERENCES llm_routes(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  observed_release_id UUID REFERENCES llm_releases(id),
  observed_run_id UUID REFERENCES llm_deployment_runs(id),
  backend_kind TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  backend_health TEXT NOT NULL DEFAULT 'unknown',
  gateway_status TEXT NOT NULL DEFAULT 'unknown',
  gateway_target TEXT NOT NULL DEFAULT '',
  gateway_config_hash TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE llm_route_observations
  ADD CONSTRAINT fk_llm_observations_route_release
  FOREIGN KEY (route_id, observed_release_id) REFERENCES llm_releases(route_id, id);

CREATE INDEX idx_llm_route_observations_route_env_observed
  ON llm_route_observations(route_id, environment_id, observed_at DESC);

CREATE TABLE llm_route_state (
  route_id UUID NOT NULL REFERENCES llm_routes(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  desired_release_id UUID REFERENCES llm_releases(id),
  desired_intent_id UUID REFERENCES llm_deployment_intents(id),
  active_run_id UUID REFERENCES llm_deployment_runs(id),
  current_observation_id UUID REFERENCES llm_route_observations(id),
  drift_status TEXT NOT NULL DEFAULT 'unknown',
  gateway_status TEXT NOT NULL DEFAULT 'unknown',
  backend_kind TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  backend_health TEXT NOT NULL DEFAULT 'unknown',
  gateway_target TEXT NOT NULL DEFAULT '',
  last_reconciled_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (route_id, environment_id)
);

ALTER TABLE llm_route_state
  ADD CONSTRAINT fk_llm_state_route_desired_release
  FOREIGN KEY (route_id, desired_release_id) REFERENCES llm_releases(route_id, id);

CREATE INDEX idx_llm_route_state_environment ON llm_route_state(environment_id);
CREATE INDEX idx_llm_route_state_drift_status ON llm_route_state(drift_status);
