# HITL Decisions — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Current decisions

- **HITL-SFRL-001 — Scope boundary:** The original artifact pass was product/protocol only. Later buckets implemented Bahia, Metiq, and local OpenClaw bridge slices, but final product verification must still respect ownership boundaries.
- **HITL-SFRL-002 — Contract source:** `docs/soulfactory-runtime-control.md` is the shared schema source for the Bahia-owned OpenClaw sidecar/control-driver path and Metiq Go bridge work.
- **HITL-SFRL-003 — Runtime lifecycle semantics:** Lifecycle completion must be driven by explicit Nostr result events (`38386` runtime-facing and `7950` Bahia-facing), never by elapsed time or relay/subscription closure.
- **HITL-SFRL-004 — OpenClaw ownership resolved:** As of 2026-05-15, direct upstream OpenClaw modifications are not accepted as product-completion evidence because OpenClaw is not maintained by this project and is unlikely to accept this PR shape. The product decision is resolved by `bahia-nrjg`: use the separately maintained Bahia-owned OpenClaw SoulFactory sidecar implemented in `bahia-8ycd.4`.

## Seeded follow-up decisions

- Decide whether advanced avatar generation, voice-provider selection, and memory tuning are part of a later customization pack or need separate first-slice acceptance criteria.
- Decide how to replace or defer any required external REST-only Bahia deployment path if implementation discovers one.
- Optional hardening: promote the documented runtime-control examples into reusable cross-runtime fixtures for future schema-drift prevention.
