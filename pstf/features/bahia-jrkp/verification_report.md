# Verification Report: bahia-jrkp

## Evidence

- `RegistryService.RecordObservation` normalizes observation hashes before persistence.
- Desired-state rows compare `EnvironmentServiceState.DesiredHash` with the normalized observed hash.
- Non-desired-state rows continue comparing desired artifact image digest with observed image digest.
- Service-state Nostr projections include `desired_hash` and `observed_hash` when available.
- Normative drift semantics are documented in `docs/deployment.md` and `docs/event-spec.md`.

## Tests Run

```text
go test ./internal/service ./internal/repository ./internal/adapters/nostr
```

Result: pass.
