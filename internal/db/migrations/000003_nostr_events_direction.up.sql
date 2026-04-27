-- Add direction and processing status columns to nostr_events.
-- direction: 'outbound' (published by Bahia) or 'inbound' (received from relays).
ALTER TABLE nostr_events ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT 'outbound';
ALTER TABLE nostr_events ADD COLUMN IF NOT EXISTS processing_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE nostr_events ADD COLUMN IF NOT EXISTS processing_error TEXT;
ALTER TABLE nostr_events ADD COLUMN IF NOT EXISTS processed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_nostr_events_direction ON nostr_events(direction);
CREATE INDEX IF NOT EXISTS idx_nostr_events_processing ON nostr_events(processing_status) WHERE processing_status != 'none';
