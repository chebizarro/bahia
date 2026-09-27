-- Expired outbound events are terminal audit rows. They are deliberately not
-- returned to 'pending' (that would replay a stale backlog into the relays);
-- they become 'not_applicable', which no binary retries.
UPDATE nostr_events
SET publish_state = 'not_applicable'
WHERE publish_state = 'expired';

ALTER TABLE nostr_events
    DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;

ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published'));
