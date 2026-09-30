-- The previous schema has a single outbox runner bound to the interop pool.
-- Pending rows for any other pool would be retried to the wrong relays there,
-- so they are taken out of the outbox (with the reason recorded) instead.
UPDATE nostr_events
SET publish_state = 'not_applicable',
    last_publish_error = 'abandoned: publish target ' || publish_target || ' removed by migration rollback'
WHERE publish_state = 'pending' AND publish_target <> '';

-- Failed rows return to the pre-000071 representation: not_applicable with the
-- 'abandoned' reason kept in last_publish_error.
ALTER TABLE nostr_events DROP CONSTRAINT IF EXISTS nostr_events_publish_state_check;
UPDATE nostr_events
SET publish_state = 'not_applicable',
    last_publish_error = CASE
        WHEN last_publish_error LIKE 'abandoned%' THEN last_publish_error
        ELSE 'abandoned: ' || last_publish_error
    END
WHERE publish_state = 'failed';
ALTER TABLE nostr_events
    ADD CONSTRAINT nostr_events_publish_state_check
    CHECK (publish_state IN ('not_applicable', 'pending', 'published'));

DROP INDEX IF EXISTS idx_nostr_events_publish_outbox;
ALTER TABLE nostr_events DROP COLUMN IF EXISTS publish_target;
CREATE INDEX IF NOT EXISTS idx_nostr_events_publish_outbox
    ON nostr_events(received_at, id)
    WHERE publish_state = 'pending';
