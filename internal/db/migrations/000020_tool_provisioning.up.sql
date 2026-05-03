CREATE TABLE tool_denylist (
  package_name TEXT NOT NULL,
  manager TEXT NOT NULL,
  reason TEXT NOT NULL,
  source TEXT,
  blocked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  blocked_by TEXT,
  PRIMARY KEY (package_name, manager)
);

CREATE TABLE tool_provision_intents (
  id UUID PRIMARY KEY,
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  requested_tools JSONB NOT NULL,
  resolved_tools JSONB,
  security_scan_results JSONB,
  toolset_hash TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  approval_required BOOLEAN NOT NULL DEFAULT false,
  approval_flags JSONB,
  approved_by TEXT,
  approved_at TIMESTAMPTZ,
  nostr_event_id TEXT,
  requester_pubkey TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tool_provision_runs (
  id UUID PRIMARY KEY,
  intent_id UUID NOT NULL REFERENCES tool_provision_intents(id) ON DELETE CASCADE,
  base_image_digest TEXT NOT NULL,
  built_image_digest TEXT,
  artifact_id UUID REFERENCES artifacts(id),
  build_log_url TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  error_message TEXT
);

CREATE TABLE tool_profile_state (
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  current_toolset_hash TEXT,
  current_image_digest TEXT,
  installed_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
  previous_image_digest TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (service_id, environment_id)
);

CREATE TABLE tool_approval_log (
  id UUID PRIMARY KEY,
  intent_id UUID NOT NULL REFERENCES tool_provision_intents(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  actor_pubkey TEXT,
  reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tool_provision_intents_service_env_created
  ON tool_provision_intents(service_id, environment_id, created_at DESC);
CREATE INDEX idx_tool_provision_intents_status
  ON tool_provision_intents(status);
CREATE INDEX idx_tool_provision_intents_pending_approval
  ON tool_provision_intents(created_at)
  WHERE status = 'awaiting_approval';
CREATE INDEX idx_tool_provision_intents_nostr_event_id
  ON tool_provision_intents(nostr_event_id);

CREATE INDEX idx_tool_provision_runs_intent_id
  ON tool_provision_runs(intent_id);
CREATE INDEX idx_tool_provision_runs_status
  ON tool_provision_runs(status);

CREATE INDEX idx_tool_profile_state_environment
  ON tool_profile_state(environment_id);

CREATE INDEX idx_tool_approval_log_intent_created
  ON tool_approval_log(intent_id, created_at DESC);
