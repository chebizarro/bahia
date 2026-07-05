# BAHIA_LOOM_SIGNET_PROJECTION Verification Report

Date: 2026-07-05

## Evidence

- Added `CanonicalSigner` interface in `internal/adapters/loom/projection.go` matching the existing Signet client `Sign(ctx, *nostr.Event)` shape.
- Added `ProjectCanonicalStatusWithSigner`, `ProjectCanonicalResultWithSigner`, and `ProjectCanonicalJobStateWithSigner` signer-first projection entry points.
- Added Loom client signer injection via `loom.WithCanonicalSigner` plus client projection methods that call the signer-first projection APIs.
- Added `loom.canonical_projection` config for enabled projection signing and Signet bunker URI/client secret; raw-key projection config fields are rejected by validation.
- Added config validation that requires Signet when projection is enabled and rejects both raw projection keys and `allow_raw_key_dev` in validated runtime configuration.
- Wired app startup to construct/connect a configured Signet/NIP-46 client for Loom canonical projection and inject it into `loom.NewClient`; enabled projection without Signet fails closed.
- Updated sample `config.yaml` to keep canonical projection disabled by default and document Signet-first projection signing.
- Added unit coverage for successful signer-first state/audit publish, missing signer rejection, signer error propagation, Loom client signer injection, config load/env mapping, raw-key runtime config rejection, and app missing-Signet failure.

## Quality gates

```bash
go test ./internal/adapters/loom
go test ./internal/config
go test ./internal/app
go test ./internal/app ./internal/controlplane ./internal/adapters/loom ./internal/config
go test ./...
```

Result: passed.

## Remaining tracked scope

No remaining work is known for `bahia-x0pbp`. Raw-key projection compatibility remains present only as a direct development/migration adapter and is unavailable in validated Bahia runtime projection configuration.
