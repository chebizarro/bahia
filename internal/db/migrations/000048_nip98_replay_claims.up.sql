CREATE TABLE nip98_replay_claims (
    event_id TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_nip98_replay_claims_expires_at ON nip98_replay_claims(expires_at);
