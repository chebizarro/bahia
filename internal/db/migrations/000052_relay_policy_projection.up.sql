CREATE TABLE IF NOT EXISTS relay_policy_projections (
  author_pubkey TEXT PRIMARY KEY,
  event_id TEXT NOT NULL UNIQUE,
  event_created_at TIMESTAMPTZ NOT NULL,
  event_accepted_at TIMESTAMPTZ NOT NULL,
  schema TEXT NOT NULL,
  canonical_payload BYTEA NOT NULL,
  payload_hash TEXT NOT NULL,
  source_relay TEXT NOT NULL DEFAULT '',
  last_sync_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT relay_policy_projection_author_hex CHECK (author_pubkey ~ '^[0-9a-f]{64}$'),
  CONSTRAINT relay_policy_projection_event_hex CHECK (event_id ~ '^[0-9a-f]{64}$'),
  CONSTRAINT relay_policy_projection_hash_hex CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX idx_relay_policy_projections_event_created
  ON relay_policy_projections(event_created_at DESC, event_id ASC);
