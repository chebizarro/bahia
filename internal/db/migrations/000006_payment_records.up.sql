-- Cashu payment records for worker job payments.
CREATE TABLE IF NOT EXISTS payment_records (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_run_id UUID NOT NULL REFERENCES deployment_runs(id) ON DELETE CASCADE,
    worker_pubkey     TEXT NOT NULL,
    mint_url          TEXT NOT NULL,
    amount_sats       BIGINT NOT NULL CHECK (amount_sats >= 0),
    token_hash        TEXT,
    direction         TEXT NOT NULL CHECK (direction IN ('payment', 'change')),
    status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'redeemed', 'failed', 'refunded')),
    error_message     TEXT,
    metadata          JSONB DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_records_run ON payment_records(deployment_run_id);
CREATE INDEX idx_payment_records_worker ON payment_records(worker_pubkey);
CREATE INDEX idx_payment_records_status ON payment_records(status);
CREATE UNIQUE INDEX idx_payment_records_token_hash ON payment_records(token_hash) WHERE token_hash IS NOT NULL;
