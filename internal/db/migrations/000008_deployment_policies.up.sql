-- Deployment policies for supply-chain enforcement.
CREATE TABLE IF NOT EXISTS deployment_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    environment_id  UUID REFERENCES environments(id) ON DELETE CASCADE,
    rules           JSONB NOT NULL DEFAULT '[]',
    enforcement     TEXT NOT NULL DEFAULT 'warn' CHECK (enforcement IN ('warn', 'block')),
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_deployment_policies_env ON deployment_policies(environment_id);
CREATE INDEX idx_deployment_policies_enabled ON deployment_policies(enabled) WHERE enabled = true;
