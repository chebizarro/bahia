# Verification Report: bahia-8epx.9

## Evidence
- `ngit init --help` was run on 2026-06-07. Installed `ngit 2.4.2` reports `--relay <RELAY>...`, verifying repeated relay arguments.
- `internal/soulfactory/workspace.go` now normalizes `NgitRelays` only from `WorkspaceConfig.NgitRelays`; it does not fall back to `OpenClawRelays`.
- `pushWithNgit` builds `ngit init` arguments with repeated `--relay` entries for every normalized ngit publication relay.

## Checks
- `ngit init --help` — passed; output showed installed `ngit 2.4.2` and `--relay <RELAY>...`.
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/soulfactory` — passed.

## Scope Boundaries
- Backend config wiring in `internal/config` and `internal/adapters/nostr` is intentionally untouched because another agent is working that relay policy slice.
