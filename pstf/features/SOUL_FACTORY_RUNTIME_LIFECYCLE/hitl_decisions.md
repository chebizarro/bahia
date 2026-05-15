# HITL Decisions — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Current decisions

- **HITL-SFRL-001 — Scope boundary:** This artifact pass is product/protocol only. Do not implement Go, JavaScript, or TypeScript runtime code in this work bucket.
- **HITL-SFRL-002 — Contract source:** `docs/soulfactory-runtime-control.md` is the shared schema source for OpenClaw TypeScript and Metiq Go bridge work.
- **HITL-SFRL-003 — Runtime lifecycle semantics:** Lifecycle completion must be driven by explicit Nostr result events (`38386` runtime-facing and `7950` Bahia-facing), never by elapsed time or relay/subscription closure.

## Seeded follow-up decisions

- Decide whether advanced avatar generation, voice-provider selection, and memory tuning are part of a later customization pack or need separate first-slice acceptance criteria.
- Decide the exact embedded-vs-sidecar OpenClaw bridge placement during implementation; the plan currently favors embedded because OpenClaw owns config/session state.
- Decide how to replace or defer any required external REST-only Bahia deployment path if implementation discovers one.
