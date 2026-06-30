# HITL decisions for bahia-i89o

## 2026-06-29 — Option B: heartbeat observations use NIP-38 status

Decision from bead `bahia-i89o`: heartbeat observations are intentionally NIP-38 operational status events on kind `30315`. Resolve the mismatch by aligning Go constants, generated web constants, continuity filters, serializer/catalog behavior, docs, and drift tests on `30315`.

Implementation rule: disambiguate continuity heartbeat observations by `#domain=continuity` plus heartbeat schema/d/worker tags. Do not restore or emit a unique production heartbeat kind `30350`.
