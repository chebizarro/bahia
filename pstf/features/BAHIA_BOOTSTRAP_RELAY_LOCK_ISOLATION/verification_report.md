# Verification report

Feature: `BAHIA_BOOTSTRAP_RELAY_LOCK_ISOLATION`

## Results

- `BBRLI-AC1`: passed. The deterministic regression stalls publication on a
  managed relay and proves subscription setup completes before publication is
  released.
- `BBRLI-AC2`: passed. The Nostr adapter package passed the race-enabled suite
  using the repository's pinned Go 1.26.3 toolchain.
- `BBRLI-AC3`: passed. The repository Dockerfile produced image
  `sha256:0d3f2691618062d09d0f0db8f75daa5a5f6a2a0ae5f83c632b7b90f655ae04b7`.
- `BBRLI-AC4`: pending canonical integration and guarded production canary.

## Quality gates

- `go test ./internal/adapters/nostr -count=1`: passed.
- `go vet ./internal/adapters/nostr`: passed.
- `go test -race -gcflags=fiatjaf.com/nostr=-d=checkptr=0 ./internal/adapters/nostr -count=1`: passed.
- `go test ./internal/app ./internal/service ./internal/reconcile -count=1`: passed.
- `go vet ./internal/app ./internal/service ./internal/reconcile`: passed.
- Repository Docker image build: passed.

## Production-path assessment

Topology changes still wait for in-flight publications, so a relay cannot be
removed and closed while its connection is in use. Publication network I/O no
longer holds the managed-relay state mutex, and active subscription bookkeeping
uses a dedicated mutex rather than the topology mutex. This preserves relay
lifetime safety while allowing bootstrap subscriptions to begin during slow
publication bursts. Production acceptance remains pending until the merged
revision passes the guarded live canary.
