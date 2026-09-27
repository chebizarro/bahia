-- Coalesce superseded replaceable/addressable outbound events. A pending event
-- whose NIP-01 coordinate already has a newer recorded revision can never
-- change relay state, so it moves to the terminal 'superseded' state instead
-- of consuming outbound budget. Older binaries only select 'pending' rows and
-- ignore superseded ones.
ALTER TABLE nostr_events
    DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;

ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published', 'expired', 'superseded'));

-- Supports the per-coordinate "is there a newer revision" lookup.
CREATE INDEX IF NOT EXISTS idx_nostr_events_kind_pubkey_created
    ON nostr_events(kind, pubkey, created_at DESC, id);
