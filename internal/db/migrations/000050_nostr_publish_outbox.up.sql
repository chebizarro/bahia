-- Use the Nostr audit log as a minimal durable outbound publish outbox.
ALTER TABLE nostr_events
    ADD COLUMN IF NOT EXISTS publish_state TEXT NOT NULL DEFAULT 'not_applicable',
    ADD COLUMN IF NOT EXISTS publish_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_publish_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ;

ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published'));

CREATE INDEX IF NOT EXISTS idx_nostr_events_publish_outbox
    ON nostr_events(received_at, id)
    WHERE publish_state = 'pending';
