-- Replay protection for NIP-98 HTTP Auth events.
-- Each successfully validated event ID is stored to prevent replay attacks.
CREATE TABLE auth_events (
  event_id   TEXT PRIMARY KEY,
  pubkey     TEXT NOT NULL,
  method     TEXT NOT NULL DEFAULT 'nip98',
  url        TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);

-- Periodically delete expired entries.
CREATE INDEX idx_auth_events_expires ON auth_events(expires_at);
