# HITL Decisions — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Current decisions

- **HITL-SFRL-001 — Scope boundary:** The original artifact pass was product/protocol only. Later buckets implemented Bahia, Metiq, and local OpenClaw bridge slices, but final product verification must still respect ownership boundaries.
- **HITL-SFRL-002 — Contract source:** `docs/soulfactory-runtime-control.md` is the shared schema source for OpenClaw TypeScript and Metiq Go bridge work.
- **HITL-SFRL-003 — Runtime lifecycle semantics:** Lifecycle completion must be driven by explicit Nostr result events (`38386` runtime-facing and `7950` Bahia-facing), never by elapsed time or relay/subscription closure.
- **HITL-SFRL-004 — OpenClaw ownership blocker:** As of 2026-05-15, direct upstream OpenClaw modifications are not accepted as product-completion evidence because OpenClaw is not maintained by this project and is unlikely to accept this PR shape. `bahia-i0rk.3` remains blocked by Beads issue `bahia-nrjg` until the product chooses maintained fork, separately maintained adapter/sidecar, or dropping OpenClaw from the first vertical slice.

## Seeded follow-up decisions

- Decide whether advanced avatar generation, voice-provider selection, and memory tuning are part of a later customization pack or need separate first-slice acceptance criteria.
- Decide how to replace or defer any required external REST-only Bahia deployment path if implementation discovers one.
- Resolve `bahia-nrjg`: choose the maintained OpenClaw ownership path before claiming SFRL-AC-005/006/008 are fully verified for OpenClaw.
