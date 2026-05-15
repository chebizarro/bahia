# HITL Decisions — OPENCLAW_SOULFACTORY_SIDECAR

- Product decision supplied on 2026-05-15: do not rely on direct upstream OpenClaw modifications; build an owned adapter/sidecar.
- Implementation decision: place the owned sidecar in Bahia and drive OpenClaw through a configurable local command-driver seam. This keeps the Nostr lifecycle contract in Bahia-owned code and avoids REST lifecycle APIs.
- No unresolved human decision is required for this slice.
