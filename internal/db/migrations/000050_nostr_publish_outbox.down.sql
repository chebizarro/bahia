DROP INDEX IF EXISTS idx_nostr_events_publish_outbox;
ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;
ALTER TABLE nostr_events
    DROP COLUMN IF EXISTS published_at,
    DROP COLUMN IF EXISTS last_publish_error,
    DROP COLUMN IF EXISTS publish_attempts,
    DROP COLUMN IF EXISTS publish_state;
