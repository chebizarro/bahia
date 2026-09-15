-- Reverse the event-idempotency semantics fix.
--
-- Note: if the current data contains multiple bindings per
-- (org_id, agent_id, service_id, release_channel, release_id) — the whole
-- reason for this migration — recreating the prior constraint will fail.
-- Operators must resolve that data (typically by preserving only the earliest
-- binding per release) before downgrading. The migration errors instead of
-- silently discarding append-only promotion history.
DROP INDEX IF EXISTS agent_service_runtime_release_bindings_chain_next_idx;
DROP INDEX IF EXISTS agent_service_runtime_release_bindings_chain_head_idx;

ALTER TABLE agent_service_runtime_release_bindings
    ADD CONSTRAINT agent_service_runtime_release_bindings_release_uniq_key
    UNIQUE (org_id, agent_id, service_id, release_channel, release_id);
