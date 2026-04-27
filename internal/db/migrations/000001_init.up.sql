-- 000001_init.up.sql
-- Creates the core schema for the Bahia Deployment Registry.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- services: deployable application components
CREATE TABLE services (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE,
  repo_url TEXT,
  artifact_repo TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  runtime_type TEXT NOT NULL DEFAULT 'docker',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- environments: named deployment targets
CREATE TABLE environments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE,
  loom_worker_selector JSONB NOT NULL DEFAULT '{}'::jsonb,
  runtime_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  deploy_strategy TEXT NOT NULL DEFAULT 'replace',
  protected BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- builds: CI build executions
CREATE TABLE builds (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  git_sha TEXT NOT NULL,
  git_ref TEXT NOT NULL,
  ci_system TEXT NOT NULL DEFAULT 'hive-ci',
  ci_run_id TEXT NOT NULL,
  loom_job_id TEXT,
  status TEXT NOT NULL,
  source_event_id TEXT,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (ci_system, ci_run_id)
);

CREATE INDEX idx_builds_service_id ON builds(service_id);
CREATE INDEX idx_builds_status ON builds(status);

-- artifacts: immutable OCI images
CREATE TABLE artifacts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  build_id UUID NOT NULL REFERENCES builds(id) ON DELETE CASCADE,
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  image_repo TEXT NOT NULL,
  image_tag TEXT NOT NULL,
  image_digest TEXT NOT NULL,
  manifest_media_type TEXT,
  size_bytes BIGINT,
  sbom_url TEXT,
  signature_ref TEXT,
  scan_status TEXT NOT NULL DEFAULT 'unknown',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (image_repo, image_digest)
);

CREATE INDEX idx_artifacts_service_id ON artifacts(service_id);
CREATE INDEX idx_artifacts_build_id ON artifacts(build_id);

-- deployment_intents: requests to deploy an artifact to an environment
CREATE TABLE deployment_intents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  artifact_id UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  requested_by TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  approval_status TEXT NOT NULL DEFAULT 'not_required',
  status TEXT NOT NULL,
  supersedes_intent_id UUID REFERENCES deployment_intents(id),
  approval_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  approved_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_deployment_intents_service_env ON deployment_intents(service_id, environment_id);
CREATE INDEX idx_deployment_intents_status ON deployment_intents(status);

-- deployment_runs: concrete execution attempts
CREATE TABLE deployment_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_intent_id UUID NOT NULL REFERENCES deployment_intents(id) ON DELETE CASCADE,
  loom_job_id TEXT,
  worker_pubkey TEXT,
  worker_name TEXT,
  status TEXT NOT NULL,
  exit_code INTEGER,
  stdout_ref TEXT,
  stderr_ref TEXT,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_deployment_runs_intent_id ON deployment_runs(deployment_intent_id);
CREATE INDEX idx_deployment_runs_status ON deployment_runs(status);

-- runtime_observations: snapshots of actual runtime state
CREATE TABLE runtime_observations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  observed_image_digest TEXT NOT NULL,
  observed_image_repo TEXT,
  observed_container_id TEXT,
  observed_host TEXT,
  observed_version TEXT,
  health_status TEXT NOT NULL,
  source TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_runtime_observations_service_env ON runtime_observations(service_id, environment_id);

-- environment_service_state: denormalized desired vs observed state
CREATE TABLE environment_service_state (
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  desired_artifact_id UUID REFERENCES artifacts(id),
  desired_intent_id UUID REFERENCES deployment_intents(id),
  last_successful_run_id UUID REFERENCES deployment_runs(id),
  current_observation_id UUID REFERENCES runtime_observations(id),
  drift_status TEXT NOT NULL DEFAULT 'unknown',
  last_reconciled_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (service_id, environment_id)
);

-- Nostr event log for audit trail
CREATE TABLE nostr_events (
  id TEXT PRIMARY KEY,
  kind INTEGER NOT NULL,
  pubkey TEXT NOT NULL,
  content TEXT NOT NULL,
  tags JSONB NOT NULL DEFAULT '[]'::jsonb,
  sig TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  entity_type TEXT,
  entity_id UUID
);

CREATE INDEX idx_nostr_events_kind ON nostr_events(kind);
CREATE INDEX idx_nostr_events_entity ON nostr_events(entity_type, entity_id);
