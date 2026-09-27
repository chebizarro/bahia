-- Superseded outbound events are terminal audit rows; they are never returned
-- to 'pending'. They become 'not_applicable', which no binary retries.
UPDATE nostr_events
SET publish_state = 'not_applicable'
WHERE publish_state = 'superseded';

ALTER TABLE nostr_events
    DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;

ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published', 'expired'));
