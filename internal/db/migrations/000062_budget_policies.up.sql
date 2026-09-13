CREATE TABLE IF NOT EXISTS budget_policies (
    id           UUID PRIMARY KEY,
    version      BIGINT NOT NULL DEFAULT 1,
    name         TEXT NOT NULL,
    agent_pubkey TEXT NOT NULL DEFAULT '',
    task_id      TEXT NOT NULL DEFAULT '',
    scope        TEXT NOT NULL CHECK (scope IN ('agent', 'task', 'both')),
    limits       JSONB NOT NULL DEFAULT '[]',
    enabled      BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_budget_policies_agent_pubkey ON budget_policies (agent_pubkey) WHERE agent_pubkey != '';
CREATE INDEX idx_budget_policies_task_id ON budget_policies (task_id) WHERE task_id != '';
CREATE INDEX idx_budget_policies_enabled ON budget_policies (enabled) WHERE enabled = true;
