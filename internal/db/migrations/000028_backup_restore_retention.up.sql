-- Durable restore and retention foundations for the backup control plane.
CREATE TABLE backup_restores (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_run_id UUID NOT NULL REFERENCES backup_runs(id) ON DELETE RESTRICT,
  recipe_id UUID NOT NULL REFERENCES backup_recipes(id) ON DELETE RESTRICT,
  repository_id UUID NOT NULL REFERENCES backup_repositories(id) ON DELETE RESTRICT,
  policy_id UUID REFERENCES backup_policies(id) ON DELETE SET NULL,
  snapshot_id TEXT NOT NULL,
  restore_target_ref TEXT NOT NULL,
  requested_by TEXT NOT NULL,
  request_event_id TEXT NOT NULL,
  request_kind INTEGER NOT NULL,
  request_d_tag TEXT NOT NULL,
  approval_status TEXT NOT NULL DEFAULT 'pending',
  approval_event_id TEXT NOT NULL DEFAULT '',
  approved_by TEXT NOT NULL DEFAULT '',
  approved_at TIMESTAMPTZ,
  approval_message TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  backend TEXT NOT NULL,
  verification_status TEXT NOT NULL DEFAULT 'pending',
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  publish_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (requested_by, request_kind, request_d_tag)
);

CREATE INDEX idx_backup_restores_backup_run_id ON backup_restores(backup_run_id);
CREATE INDEX idx_backup_restores_recipe_id ON backup_restores(recipe_id);
CREATE INDEX idx_backup_restores_repository_id ON backup_restores(repository_id);
CREATE INDEX idx_backup_restores_policy_id ON backup_restores(policy_id);
CREATE INDEX idx_backup_restores_status ON backup_restores(status);
CREATE INDEX idx_backup_restores_approval_status ON backup_restores(approval_status);
CREATE INDEX idx_backup_restores_request_event_id ON backup_restores(request_event_id);

CREATE TABLE backup_retention_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  repository_id UUID NOT NULL REFERENCES backup_repositories(id) ON DELETE RESTRICT,
  policy_id UUID REFERENCES backup_policies(id) ON DELETE SET NULL,
  requested_by TEXT NOT NULL,
  request_event_id TEXT NOT NULL,
  request_kind INTEGER NOT NULL,
  request_d_tag TEXT NOT NULL,
  status TEXT NOT NULL,
  backend TEXT NOT NULL,
  dry_run BOOLEAN NOT NULL DEFAULT false,
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  publish_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (requested_by, request_kind, request_d_tag)
);

CREATE INDEX idx_backup_retention_runs_repository_id ON backup_retention_runs(repository_id);
CREATE INDEX idx_backup_retention_runs_policy_id ON backup_retention_runs(policy_id);
CREATE INDEX idx_backup_retention_runs_status ON backup_retention_runs(status);
CREATE INDEX idx_backup_retention_runs_request_event_id ON backup_retention_runs(request_event_id);
