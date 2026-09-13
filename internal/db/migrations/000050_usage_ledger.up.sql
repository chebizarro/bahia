CREATE TABLE IF NOT EXISTS usage_ledger_records (
    id UUID PRIMARY KEY,
    agent_pubkey TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL,
    amount BIGINT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    recorded_by TEXT NOT NULL,
    signature TEXT NOT NULL,
    correction_of UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT unique_usage_event UNIQUE (agent_pubkey, task_id, resource_type, recorded_at, recorded_by),
    CONSTRAINT fk_correction_of FOREIGN KEY (correction_of) REFERENCES usage_ledger_records(id) ON DELETE CASCADE,
    CONSTRAINT chk_resource_type CHECK (resource_type IN ('compute', 'inference', 'storage')),
    CONSTRAINT chk_amount_positive CHECK (amount > 0),
    CONSTRAINT chk_not_self_correction CHECK (correction_of IS NULL OR correction_of != id)
);

CREATE INDEX idx_usage_ledger_agent_pubkey ON usage_ledger_records(agent_pubkey);
CREATE INDEX idx_usage_ledger_task_id ON usage_ledger_records(task_id);
CREATE INDEX idx_usage_ledger_resource_type ON usage_ledger_records(resource_type);
CREATE INDEX idx_usage_ledger_recorded_at ON usage_ledger_records(recorded_at);
CREATE INDEX idx_usage_ledger_correction_of ON usage_ledger_records(correction_of);
