-- Bound the durable publish outbox's retry lifetime. A pending outbound event
-- whose retry lifetime (measured from durable enqueue, received_at) elapses is
-- moved to the terminal 'expired' state instead of being retried forever.
-- Older binaries only select 'pending' rows, so they ignore expired rows and
-- remain rollback-compatible.
ALTER TABLE nostr_events
    DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;

ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published', 'expired'));
