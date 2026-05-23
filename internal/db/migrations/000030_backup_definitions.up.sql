-- Canonical operator-facing backup definitions.
CREATE TABLE backup_definitions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  repository_id UUID NOT NULL REFERENCES backup_repositories(id) ON DELETE RESTRICT,
  repository_name TEXT NOT NULL DEFAULT '',
  policy_id UUID NOT NULL REFERENCES backup_policies(id) ON DELETE RESTRICT,
  policy_name TEXT NOT NULL DEFAULT '',
  recipe_id UUID NOT NULL REFERENCES backup_recipes(id) ON DELETE RESTRICT,
  recipe_name TEXT NOT NULL DEFAULT '',
  recipe_version TEXT NOT NULL DEFAULT '',
  schedule_expression TEXT NOT NULL DEFAULT '',
  schedule_enabled BOOLEAN NOT NULL DEFAULT true,
  schedule_jitter_window TEXT NOT NULL DEFAULT '',
  tenant_id UUID REFERENCES organizations(id) ON DELETE SET NULL,
  tenant_name TEXT NOT NULL DEFAULT '',
  environment_id UUID REFERENCES environments(id) ON DELETE SET NULL,
  environment_name TEXT NOT NULL DEFAULT '',
  owner_pubkey TEXT NOT NULL DEFAULT '',
  requires_approval BOOLEAN NOT NULL DEFAULT false,
  approval_policy TEXT NOT NULL DEFAULT '',
  restore_target_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
  executor_labels JSONB NOT NULL DEFAULT '[]'::jsonb,
  capability_requirements JSONB NOT NULL DEFAULT '[]'::jsonb,
  labels JSONB NOT NULL DEFAULT '{}'::jsonb,
  group_name TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by TEXT NOT NULL,
  UNIQUE (name)
);

CREATE INDEX idx_backup_definitions_repository_id ON backup_definitions(repository_id);
CREATE INDEX idx_backup_definitions_policy_id ON backup_definitions(policy_id);
CREATE INDEX idx_backup_definitions_recipe_id ON backup_definitions(recipe_id);
CREATE INDEX idx_backup_definitions_tenant_id ON backup_definitions(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE INDEX idx_backup_definitions_environment_id ON backup_definitions(environment_id) WHERE environment_id IS NOT NULL;
CREATE INDEX idx_backup_definitions_schedule_enabled ON backup_definitions(schedule_enabled) WHERE schedule_enabled = true;
CREATE INDEX idx_backup_definitions_group_name ON backup_definitions(group_name) WHERE group_name <> '';
CREATE INDEX idx_backup_definitions_labels ON backup_definitions USING GIN (labels);
CREATE INDEX idx_backup_definitions_executor_labels ON backup_definitions USING GIN (executor_labels);
CREATE INDEX idx_backup_definitions_capability_requirements ON backup_definitions USING GIN (capability_requirements);
