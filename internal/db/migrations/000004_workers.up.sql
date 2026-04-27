-- Loom worker catalog (discovered via Kind 10100 advertisements).
CREATE TABLE workers (
  pubkey             TEXT PRIMARY KEY,
  name               TEXT NOT NULL DEFAULT '',
  description        TEXT NOT NULL DEFAULT '',
  architecture       TEXT NOT NULL DEFAULT '',
  max_concurrent_jobs INTEGER NOT NULL DEFAULT 1,
  current_queue_depth INTEGER NOT NULL DEFAULT 0,
  software           JSONB NOT NULL DEFAULT '[]'::jsonb,
  pricing            JSONB NOT NULL DEFAULT '[]'::jsonb,
  min_duration_secs  INTEGER NOT NULL DEFAULT 0,
  max_duration_secs  INTEGER NOT NULL DEFAULT 0,
  geohash            TEXT NOT NULL DEFAULT '',
  preferred_relays   JSONB NOT NULL DEFAULT '[]'::jsonb,
  last_advertisement_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  status             TEXT NOT NULL DEFAULT 'online',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workers_status ON workers(status);
CREATE INDEX idx_workers_last_ad ON workers(last_advertisement_at);
