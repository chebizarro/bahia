# Unrestricted Relay Sidecar Verification

The Bahia relay sidecar no longer classifies event kinds for `EVENT`, `REQ`, or `COUNT` admission. Author identity, recipient tags, legacy/canonical classification, and filter scope do not affect relay acceptance.

Protocol validation remains in place for event IDs, Schnorr signatures, and timestamp bounds. NIP-50 search remains unavailable because the store does not implement it.

Verification command:

```text
go test ./internal/relaysidecar
```

The final result is recorded in the change handoff.

Result: PASS on 2026-07-23 using the repository's Go 1.26.3 containerized build environment:

```text
ok github.com/openagentsinc/bahia/internal/relaysidecar 0.051s
```

The full builder target also completed successfully, including `cmd/server`, `cmd/cli`, `cmd/relay`, `cmd/fips-bahia-bridge`, `cmd/openclaw-soulfactory-sidecar`, and `cmd/openclaw-soulfactory-control`.
