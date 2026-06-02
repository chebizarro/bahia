# HITL Decisions — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## HITL-001 — Backend ContextVM handler cutover

**Question:** Should CLI/pkg client operator paths switch immediately to kind `25910` ContextVM publication even though backend handlers for all legacy request families are not yet present?

**Decision needed:** Product/architecture owner must confirm the cutover sequence.

**Current slice resolution:** Do not create fake client behavior. Web encrypted transport now emits ContextVM JSON-RPC by default, docs define the intended method surface, and `pkg/client` keeps legacy request-kind publication isolated with an explicit dependency on `bahia-viys`.

**Dependency beads:** `bahia-viys`, `bahia-itrq`.
