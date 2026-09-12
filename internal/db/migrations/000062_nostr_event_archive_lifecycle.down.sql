ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_archive_batch_id_fkey;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS archive_batch_id;
DROP TABLE IF EXISTS nostr_event_archive_batches;
