DROP INDEX IF EXISTS idx_nostr_events_processing;
DROP INDEX IF EXISTS idx_nostr_events_direction;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS processed_at;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS processing_error;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS processing_status;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS direction;
