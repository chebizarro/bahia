-- Tie each outbound outbox row to the relay pool it is delivered to, and give
-- terminal delivery failures their own publish state.
--
-- nostr_events is large (see docs/architecture/postgres-event-store-lifecycle.md) and
-- this runs at startup, so every statement here is metadata-only or touches
-- only the pending outbox:
-- - the column has a constant default (no table rewrite);
-- - the only UPDATE is restricted to pending rows, served by the existing
-- partial idx_nostr_events_publish_outbox index;
-- - the widened CHECK is added NOT VALID. Existing rows already satisfy the
-- narrower 000050 constraint; the archive maintenance command validates it
-- online (EnsureOnlineIndexes), as 000062 does for its foreign key;
-- - no index is built: runners filter publish_target on top of the existing
-- pending partial index, whose row set is tiny.

-- publish_target names the pool ('' = the daemon interop pool, the runner that
-- owned every row before targets existed; 'control-plane' = the control-plane
-- pool). Each publisher runner only drains rows for its own target.
ALTER TABLE nostr_events
    ADD COLUMN IF NOT EXISTS publish_target TEXT NOT NULL DEFAULT '';

-- Config-fabric desired state was always published to the control-plane
-- relays; rows still pending must not be retried to the interop relays.
UPDATE nostr_events
SET publish_target = 'control-plane'
WHERE publish_state = 'pending' AND entity_type = 'config-fabric.desired';

-- 'failed' marks outbound rows whose delivery was given up on.
ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;
ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published', 'failed'))
    NOT VALID;
