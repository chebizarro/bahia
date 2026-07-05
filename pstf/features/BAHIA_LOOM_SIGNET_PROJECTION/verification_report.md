# BAHIA_LOOM_SIGNET_PROJECTION Verification Report

Date: 2026-07-05

## Evidence

- Added `CanonicalSigner` interface in `internal/adapters/loom/projection.go` matching the existing Signet client `Sign(ctx, *nostr.Event)` shape.
- Added `ProjectCanonicalStatusWithSigner`, `ProjectCanonicalResultWithSigner`, and `ProjectCanonicalJobStateWithSigner` signer-first projection entry points.
- Isolated raw-key compatibility behind `HexKeyCanonicalSigner`; legacy `ProjectCanonical*` functions now delegate through that adapter.
- Added unit coverage for successful signer-first state/audit publish, missing signer rejection, and signer error propagation.

## Quality gate

```bash
go test ./internal/adapters/loom
```

Result: passed.

## Remaining tracked scope

Full production wiring should inject the existing Signet client into the caller that invokes Loom canonical projection, then remove or dev-gate raw-key projection configuration once all production call sites are migrated.
