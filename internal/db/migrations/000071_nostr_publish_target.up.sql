-- Tie each outbound outbox row to the relay pool it is delivered to, and give
-- terminal delivery failures their own publish state.
--
-- publish_target names the pool ('' = the daemon interop pool, the runner that
-- owned every row before targets existed; 'control-plane' = the control-plane
-- pool). Each publisher runner only drains rows for its own target, so a row is
-- never retried to another pool's relays. Adding a column with a constant
-- default is metadata-only.
ALTER TABLE nostr_events
    ADD COLUMN IF NOT EXISTS publish_target TEXT NOT NULL DEFAULT '';

-- Config-fabric desired state was always published to the control-plane
-- relays; rows still pending must not be retried to the interop relays.
UPDATE nostr_events
SET publish_target = 'control-plane'
WHERE publish_state = 'pending' AND entity_type = 'config-fabric.desired';

-- 'failed' replaces not_applicable + an 'abandoned' reason for outbound rows
-- whose delivery was given up on, so they are distinguishable from inbound rows
-- without parsing last_publish_error.
ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;
UPDATE nostr_events
SET publish_state = 'failed'
WHERE publish_state = 'not_applicable' AND last_publish_error LIKE 'abandoned%';
ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published', 'failed'));

-- Runner discovery pages one target's pending rows in (received_at, id) order.
DROP INDEX IF EXISTS idx_nostr_events_publish_outbox;
CREATE INDEX IF NOT EXISTS idx_nostr_events_publish_outbox
    ON nostr_events(publish_target, received_at, id)
    WHERE publish_state = 'pending';
