# Verification report — bahia-i89o

Date: 2026-06-30

## Observed behavior

- `internal/kinds/kinds.go` defined `HeartbeatObservation = 30350` while `web/src/lib/nostr/kinds.gen.js` exported `HEARTBEAT_OBSERVATION = 30315`.
- `internal/kinds/generated_drift_test.go` carried a heartbeat-specific frontend override to paper over the mismatch.
- Continuity heartbeat serializer/catalog code used the heartbeat symbol in backend paths even though the HITL decision requires NIP-38 status kind `30315` with `#domain=continuity`.

## Intended behavior

- `HeartbeatObservation` is a semantic alias for NIP-38 status kind `30315`.
- Backend heartbeat serialization emits kind `30315` with `domain=continuity`, `schema=bahia.status.continuity-heartbeat.v1`, heartbeat `d`/`worker` tags, sequence and interval tags, and no production `30350`/legacy-kind tag.
- Catalog projection treats shared kind `30315` as heartbeat only when `#domain=continuity` and heartbeat schema/d/worker tags identify it; unrelated `30315` status domains remain generic status projections.
- Web continuity filters use kind `30315` with `#domain=continuity` and never kind `30350`.

## Verification

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/kinds ./internal/adapters/nostr` — passed.
- `npm --prefix web run test:unit -- --run tests/unit/continuity-nostr.test.ts` — passed.
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/controlplane ./internal/relaysidecar` — passed.
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/nostrmigration ./internal/kinds ./internal/adapters/nostr ./internal/controlplane ./internal/relaysidecar` — passed.

## Remaining work

Do not close bead `bahia-i89o`; user requested human verification first.
