-- Fix the append-only binding history semantics so A→B→A promotions record
-- three durable events. The prior UNIQUE (org_id, agent_id, service_id,
-- release_channel, release_id) constraint incorrectly treated re-promoting a
-- previously-used release as a duplicate, silently dropped by ON CONFLICT.
--
-- New semantics:
--   * source_event_id is the idempotency key (already UNIQUE).
--   * A source-event-fresh promotion always appends a new binding, even when
--     it re-uses a prior release_id.
--   * Concurrent producers cannot fork the chain — the linear-history indexes
--     enforce at most one binding per (agent, service, channel) that either
--     starts the chain (previous_binding_id IS NULL) or descends from any
--     given prior head (previous_binding_id = X).
DO $$
DECLARE cname text;
BEGIN
    SELECT conname INTO cname
    FROM pg_constraint c
    WHERE c.conrelid = 'agent_service_runtime_release_bindings'::regclass
      AND c.contype = 'u'
      AND (
            SELECT array_agg(a.attname::text ORDER BY array_position(c.conkey, a.attnum))
            FROM pg_attribute a
            WHERE a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
          ) = ARRAY['org_id','agent_id','service_id','release_channel','release_id'];
    IF cname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE agent_service_runtime_release_bindings DROP CONSTRAINT %I', cname);
    END IF;
END$$;

CREATE UNIQUE INDEX agent_service_runtime_release_bindings_chain_head_idx
    ON agent_service_runtime_release_bindings (org_id, agent_id, service_id, release_channel)
    WHERE previous_binding_id IS NULL;

CREATE UNIQUE INDEX agent_service_runtime_release_bindings_chain_next_idx
    ON agent_service_runtime_release_bindings (org_id, agent_id, service_id, release_channel, previous_binding_id)
    WHERE previous_binding_id IS NOT NULL;
