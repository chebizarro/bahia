-- Explicit operator rollback only (never run at startup).
--
-- Row updates come first, while only row locks are held, so writers are not
-- blocked; the ALTER TABLE statements after them are metadata-only and hold
-- ACCESS EXCLUSIVE only briefly before commit.

-- The previous schema has a single outbox runner bound to the interop pool.
-- Pending rows for any other pool would be retried to the wrong relays there,
-- so they are taken out of the outbox with the reason recorded. Served by the
-- pending partial index.
UPDATE nostr_events
SET publish_state = 'not_applicable',
    last_publish_error = 'abandoned: publish target ' || publish_target || ' removed by migration rollback'
WHERE publish_state = 'pending' AND publish_target <> '';

-- The restored constraint has no 'failed' state, and any later update of such
-- a row (for example archive claiming) would violate it. Failed rows are rare
-- terminal outbound rows; their reason stays in last_publish_error.
UPDATE nostr_events
SET publish_state = 'not_applicable'
WHERE publish_state = 'failed';

ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;
ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published'))
    NOT VALID;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS publish_target;
