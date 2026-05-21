-- Backup control-plane schema. Bahia remains the orchestration/provenance control plane.
CREATE TABLE backup_repositories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  backend TEXT NOT NULL,
  repository_uri TEXT NOT NULL,
  credential_profile TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name)
);

CREATE INDEX idx_backup_repositories_backend ON backup_repositories(backend);

CREATE TABLE backup_policies (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  require_verification BOOLEAN NOT NULL DEFAULT false,
  verification_mode TEXT NOT NULL DEFAULT 'none',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name)
);

CREATE TABLE backup_recipes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  backend TEXT NOT NULL,
  repository_id UUID NOT NULL REFERENCES backup_repositories(id) ON DELETE RESTRICT,
  policy_id UUID REFERENCES backup_policies(id) ON DELETE SET NULL,
  target_ref TEXT NOT NULL,
  include_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
  exclude_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
  verification_mode TEXT NOT NULL DEFAULT 'none',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);

CREATE INDEX idx_backup_recipes_repository_id ON backup_recipes(repository_id);
CREATE INDEX idx_backup_recipes_policy_id ON backup_recipes(policy_id);
CREATE INDEX idx_backup_recipes_backend ON backup_recipes(backend);

CREATE TABLE backup_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  recipe_id UUID NOT NULL REFERENCES backup_recipes(id) ON DELETE RESTRICT,
  repository_id UUID NOT NULL REFERENCES backup_repositories(id) ON DELETE RESTRICT,
  policy_id UUID REFERENCES backup_policies(id) ON DELETE SET NULL,
  requested_by TEXT NOT NULL,
  request_event_id TEXT NOT NULL,
  request_kind INTEGER NOT NULL,
  request_d_tag TEXT NOT NULL,
  status TEXT NOT NULL,
  backend TEXT NOT NULL,
  target_ref TEXT NOT NULL,
  snapshot_created BOOLEAN NOT NULL DEFAULT false,
  snapshot_id TEXT NOT NULL DEFAULT '',
  verification_status TEXT NOT NULL DEFAULT 'pending',
  publish_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (requested_by, request_kind, request_d_tag)
);

CREATE INDEX idx_backup_runs_recipe_id ON backup_runs(recipe_id);
CREATE INDEX idx_backup_runs_repository_id ON backup_runs(repository_id);
CREATE INDEX idx_backup_runs_policy_id ON backup_runs(policy_id);
CREATE INDEX idx_backup_runs_status ON backup_runs(status);
CREATE INDEX idx_backup_runs_request_event_id ON backup_runs(request_event_id);

CREATE TABLE backup_verifications (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_run_id UUID NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  verified BOOLEAN NOT NULL DEFAULT false,
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NOT NULL DEFAULT '',
  publish_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (backup_run_id)
);

CREATE INDEX idx_backup_verifications_status ON backup_verifications(status);
