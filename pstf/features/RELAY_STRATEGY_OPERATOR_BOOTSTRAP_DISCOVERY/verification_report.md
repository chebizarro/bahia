# Verification Report — RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY

Date: 2026-06-07
Beads: `bahia-8epx.6`, `bahia-8epx.6.1`, `bahia-8epx.6.2`, `bahia-8epx.6.3`

## Intended behavior

Operator final relay precedence is preserved: explicit CLI `--relay` values are highest priority, `BAHIA_NOSTR_RELAYS` is second, and trusted bootstrap discovery runs only when both final relay sources are absent and both bootstrap relays plus trusted service pubkeys are configured.

## Evidence

- `cmd/cli/operator_nostr.go` resolves final relays before discovery and exposes deterministic errors for missing final/discovery configuration.
- `cmd/cli/main.go` exposes `--bootstrap-relay` and `--trusted-service-pubkey`; existing `--service-pubkey` remains request routing and can provide single-service discovery trust.
- `pkg/client/operator_discovery.go` subscribes to trusted service-authored NIP-51 `30002` relay sets scoped to `#d=[bahia-contextvm-v1,bahia-browser-v1]`, validates event ID/signature/timestamp, respects parameterized replaceable latest-by-author-and-d semantics, waits for EOSE, dedupes events, prefers `bahia-contextvm-v1`, and falls back to `bahia-browser-v1` only when no usable ContextVM set is present.
- `docs/user-guide/cli-reference.md` documents operator relay precedence and trusted bootstrap discovery requirements.

## Checks

- `go test ./pkg/client ./cmd/cli` — passed.
- `python3 -m json.tool pstf/features/RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY/feature_spec.json` — passed.

## Out of scope

- NIP-42 AUTH behavior for operator/bootstrap relays remains owned by `bahia-8epx.7`.
- NIP-34 repository relay routing remains owned by `bahia-8epx.8`; no `web/` repository files were changed.
- NIP-86 relay administration remains owned by `bahia-8epx.10`.
