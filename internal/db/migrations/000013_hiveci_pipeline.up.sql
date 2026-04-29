-- 000013_hiveci_pipeline.up.sql
-- Adds Hive-CI workflow ingestion tables and pipeline mapping policy.

CREATE TABLE hiveci_workflow_runs (
  run_event_id TEXT PRIMARY KEY,
  repo_coordinate TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  branch TEXT NOT NULL,
  workflow_path TEXT NOT NULL,
  trigger_type TEXT,
  triggered_by TEXT,
  publisher_pubkey TEXT NOT NULL,
  processing_state TEXT NOT NULL DEFAULT 'pending_result',
  processing_error TEXT,
  event_created_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_hiveci_workflow_runs_repo ON hiveci_workflow_runs(repo_coordinate);
CREATE INDEX idx_hiveci_workflow_runs_state ON hiveci_workflow_runs(processing_state);

CREATE TABLE hiveci_workflow_results (
  result_event_id TEXT PRIMARY KEY,
  run_event_id TEXT NOT NULL,
  status TEXT NOT NULL,
  exit_code INTEGER NOT NULL,
  duration_seconds INTEGER NOT NULL,
  log_url TEXT,
  error TEXT,
  publisher_pubkey TEXT NOT NULL,
  processing_state TEXT NOT NULL DEFAULT 'pending_run',
  processing_error TEXT,
  event_created_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_event_id)
);

CREATE INDEX idx_hiveci_workflow_results_state ON hiveci_workflow_results(processing_state);

CREATE TABLE hiveci_pipeline_policies (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  repo_coordinate TEXT NOT NULL,
  workflow_path TEXT NOT NULL,
  branch_pattern TEXT,
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  enabled BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_coordinate, workflow_path, branch_pattern, service_id, environment_id)
);

CREATE INDEX idx_hiveci_pipeline_policies_service_env ON hiveci_pipeline_policies(service_id, environment_id);
CREATE INDEX idx_hiveci_pipeline_policies_enabled ON hiveci_pipeline_policies(enabled);

CREATE UNIQUE INDEX idx_deployment_intents_hiveci_run_id
  ON deployment_intents ((metadata->>'hive_ci_run_event_id'))
  WHERE source_kind = 'auto_promote' AND metadata ? 'hive_ci_run_event_id';
