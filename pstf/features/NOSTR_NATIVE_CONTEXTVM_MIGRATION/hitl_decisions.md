# HITL Decisions — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## HITL-001 — Backend ContextVM handler cutover

**Question:** Should CLI/pkg client operator paths switch immediately to kind `25910` ContextVM publication even though backend handlers for all legacy request families are not yet present?

**Decision:** Resolved by completed sibling streams. `bahia-dgju` implemented CEP-4/NIP-59 gift-wrap around inner ContextVM `25910`; `bahia-f0uw` removed production legacy runtime kind reactor/subscriber support outside startup migration; `bahia-viys` completed web/CLI client cutover to ContextVM/canonical observables.

**Final resolution:** Production mutation paths use ContextVM JSON-RPC kind `25910`, normally wrapped with `1059`/`21059` where encrypted transport is available. Legacy Bahia custom kinds are migration/test fixtures only, not live runtime support.

**Dependency beads:** none remaining for this feature.
