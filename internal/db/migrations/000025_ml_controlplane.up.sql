-- Generic AI/ML control-plane schema. Additive to the existing LLM compatibility tables.
CREATE TABLE ml_models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  family TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  modalities JSONB NOT NULL DEFAULT '[]'::jsonb,
  task_kinds JSONB NOT NULL DEFAULT '[]'::jsonb,
  capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
  license TEXT NOT NULL DEFAULT '',
  source JSONB NOT NULL DEFAULT '{}'::jsonb,
  card JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_models_task_kinds ON ml_models USING GIN (task_kinds);

CREATE TABLE ml_model_versions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_id UUID NOT NULL REFERENCES ml_models(id) ON DELETE CASCADE,
  version TEXT NOT NULL,
  source JSONB NOT NULL DEFAULT '{}'::jsonb,
  runtime_requirements JSONB NOT NULL DEFAULT '{}'::jsonb,
  aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
  artifact_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_id, version),
  UNIQUE (model_id, id)
);

CREATE INDEX idx_ml_model_versions_model_id ON ml_model_versions(model_id);

CREATE TABLE ml_artifact_refs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_version_id UUID REFERENCES ml_model_versions(id) ON DELETE SET NULL,
  kind TEXT NOT NULL,
  format TEXT NOT NULL,
  uri TEXT NOT NULL,
  sha256 TEXT NOT NULL DEFAULT '',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  media_type TEXT NOT NULL DEFAULT '',
  source JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_artifact_refs_model_version ON ml_artifact_refs(model_version_id);
CREATE INDEX idx_ml_artifact_refs_sha256 ON ml_artifact_refs(sha256) WHERE sha256 <> '';
CREATE INDEX idx_ml_artifact_refs_format ON ml_artifact_refs(format);

CREATE TABLE ml_provenance_edges (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  from_artifact_id UUID REFERENCES ml_artifact_refs(id) ON DELETE SET NULL,
  to_artifact_id UUID REFERENCES ml_artifact_refs(id) ON DELETE SET NULL,
  model_version_id UUID REFERENCES ml_model_versions(id) ON DELETE SET NULL,
  edge_kind TEXT NOT NULL,
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  verified BOOLEAN NOT NULL DEFAULT false,
  defect TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_provenance_edges_from ON ml_provenance_edges(from_artifact_id);
CREATE INDEX idx_ml_provenance_edges_to ON ml_provenance_edges(to_artifact_id);
CREATE INDEX idx_ml_provenance_edges_model_version ON ml_provenance_edges(model_version_id);

CREATE TABLE ml_recipes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  yaml TEXT NOT NULL DEFAULT '',
  normalized_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  inputs JSONB NOT NULL DEFAULT '{}'::jsonb,
  steps JSONB NOT NULL DEFAULT '[]'::jsonb,
  outputs JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);

CREATE TABLE ml_recipe_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  recipe_id UUID NOT NULL REFERENCES ml_recipes(id) ON DELETE CASCADE,
  requested_by TEXT NOT NULL,
  status TEXT NOT NULL,
  inputs JSONB NOT NULL DEFAULT '{}'::jsonb,
  parameters JSONB NOT NULL DEFAULT '{}'::jsonb,
  step_states JSONB NOT NULL DEFAULT '[]'::jsonb,
  result JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_recipe_runs_recipe_id ON ml_recipe_runs(recipe_id);
CREATE INDEX idx_ml_recipe_runs_status ON ml_recipe_runs(status);

CREATE TABLE ml_inference_endpoints (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  task_kinds JSONB NOT NULL DEFAULT '[]'::jsonb,
  protocol TEXT NOT NULL DEFAULT '',
  gateway JSONB NOT NULL DEFAULT '{}'::jsonb,
  placement_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name, environment_id)
);

CREATE INDEX idx_ml_inference_endpoints_environment ON ml_inference_endpoints(environment_id);
CREATE INDEX idx_ml_inference_endpoints_task_kinds ON ml_inference_endpoints USING GIN (task_kinds);

CREATE TABLE ml_deployment_intents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  endpoint_id UUID NOT NULL REFERENCES ml_inference_endpoints(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  model_version_id UUID NOT NULL REFERENCES ml_model_versions(id) ON DELETE CASCADE,
  requested_by TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  approval_status TEXT NOT NULL DEFAULT 'not_required',
  status TEXT NOT NULL,
  runtime_preference TEXT NOT NULL DEFAULT '',
  supersedes_intent_id UUID REFERENCES ml_deployment_intents(id),
  approval_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  approved_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_deployment_intents_endpoint_env ON ml_deployment_intents(endpoint_id, environment_id);
CREATE INDEX idx_ml_deployment_intents_model_version ON ml_deployment_intents(model_version_id);
CREATE INDEX idx_ml_deployment_intents_status ON ml_deployment_intents(status);

CREATE TABLE ml_deployment_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_intent_id UUID NOT NULL REFERENCES ml_deployment_intents(id) ON DELETE CASCADE,
  runtime_kind TEXT NOT NULL DEFAULT '',
  endpoint_ref TEXT NOT NULL DEFAULT '',
  worker_pubkey TEXT NOT NULL DEFAULT '',
  worker_name TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  exit_code INTEGER,
  stdout_ref TEXT NOT NULL DEFAULT '',
  stderr_ref TEXT NOT NULL DEFAULT '',
  verified_digests JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_deployment_runs_intent_id ON ml_deployment_runs(deployment_intent_id);
CREATE INDEX idx_ml_deployment_runs_status ON ml_deployment_runs(status);
CREATE UNIQUE INDEX idx_ml_deployment_runs_active_intent
  ON ml_deployment_runs(deployment_intent_id)
  WHERE status IN ('queued', 'running');

CREATE TABLE ml_inference_observations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  endpoint_id UUID NOT NULL REFERENCES ml_inference_endpoints(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  observed_model_version_id UUID REFERENCES ml_model_versions(id),
  observed_run_id UUID REFERENCES ml_deployment_runs(id),
  runtime_kind TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  backend_health TEXT NOT NULL DEFAULT 'unknown',
  gateway_status TEXT NOT NULL DEFAULT 'unknown',
  gateway_target TEXT NOT NULL DEFAULT '',
  gateway_config_hash TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_inference_observations_endpoint_env_observed
  ON ml_inference_observations(endpoint_id, environment_id, observed_at DESC);

CREATE TABLE ml_inference_state (
  endpoint_id UUID NOT NULL REFERENCES ml_inference_endpoints(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  desired_model_version_id UUID REFERENCES ml_model_versions(id),
  desired_intent_id UUID REFERENCES ml_deployment_intents(id),
  active_run_id UUID REFERENCES ml_deployment_runs(id),
  current_observation_id UUID REFERENCES ml_inference_observations(id),
  drift_status TEXT NOT NULL DEFAULT 'unknown',
  gateway_status TEXT NOT NULL DEFAULT 'unknown',
  runtime_kind TEXT NOT NULL DEFAULT '',
  backend_endpoint TEXT NOT NULL DEFAULT '',
  backend_health TEXT NOT NULL DEFAULT 'unknown',
  gateway_target TEXT NOT NULL DEFAULT '',
  last_reconciled_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (endpoint_id, environment_id)
);

CREATE INDEX idx_ml_inference_state_environment ON ml_inference_state(environment_id);
CREATE INDEX idx_ml_inference_state_drift_status ON ml_inference_state(drift_status);

CREATE TABLE ml_evaluation_specs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  task_kinds JSONB NOT NULL DEFAULT '[]'::jsonb,
  dataset_ref TEXT NOT NULL DEFAULT '',
  metrics JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);

CREATE TABLE ml_evaluation_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  spec_id UUID NOT NULL REFERENCES ml_evaluation_specs(id) ON DELETE CASCADE,
  model_version_id UUID NOT NULL REFERENCES ml_model_versions(id) ON DELETE CASCADE,
  endpoint_id UUID REFERENCES ml_inference_endpoints(id) ON DELETE SET NULL,
  status TEXT NOT NULL,
  metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
  artifacts JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ml_evaluation_runs_model_version ON ml_evaluation_runs(model_version_id);
CREATE INDEX idx_ml_evaluation_runs_status ON ml_evaluation_runs(status);
