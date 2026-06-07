# Verification Report — RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY

Date: 2026-06-07
Beads: `bahia-8epx.6`, `bahia-8epx.6.1`, `bahia-8epx.6.2`, `bahia-8epx.6.3`, `bahia-8epx.15`

## Intended behavior

Operator final relay precedence is preserved: explicit CLI `--relay` values are highest priority, `BAHIA_NOSTR_RELAYS` is second, and trusted bootstrap discovery runs only when both final relay sources are absent and both bootstrap relays plus trusted service pubkeys are configured.

Bootstrap discovery remains EOSE-driven. The bounded discovery wait is only a fail-closed guard for stalled or unavailable bootstrap transport; it is not completion semantics, and a usable relay-set candidate received before EOSE must not produce relays if the guard expires or is canceled before EOSE.

Multiple trusted service pubkeys are allowed only as an ordered allowlist for deterministic operator trust configuration. They do not imply broad service-key rotation semantics: discovery prefers `bahia-contextvm-v1` over `bahia-browser-v1`, then selects the first configured trusted pubkey with a usable set, and applies latest-wins only within the same pubkey and `d` tag.

## Evidence

- `cmd/cli/operator_nostr.go` resolves final relays before discovery and exposes deterministic errors for missing final/discovery configuration.
- `cmd/cli/main.go` exposes `--bootstrap-relay` and `--trusted-service-pubkey`; existing `--service-pubkey` remains request routing and can provide single-service discovery trust.
- `pkg/client/operator_discovery.go` subscribes to trusted service-authored NIP-51 `30002` relay sets scoped to `#d=[bahia-contextvm-v1,bahia-browser-v1]`, validates event ID/signature/timestamp, waits for EOSE before selecting relays, dedupes events, treats timeout/cancel before EOSE as fail-closed transport guard failure, prefers `bahia-contextvm-v1`, and falls back to `bahia-browser-v1` only when no usable ContextVM set is present.
- `pkg/client/operator_discovery.go` records candidates by trusted service pubkey plus `d` tag, so latest-wins is scoped to parameterized replaceable events from the same author rather than cross-key timestamp comparison, and equal `created_at` ties keep the lowest event ID.
- `docs/user-guide/cli-reference.md` documents operator relay precedence, trusted bootstrap discovery requirements, fail-closed wait semantics, and ordered multi-trust selection.

## Checks

- `GOCACHE=/tmp/bahia-go-cache go test ./pkg/client ./cmd/cli` — passed.
- `python3 -m json.tool pstf/features/RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY/feature_spec.json` — passed.
- `python3 -m json.tool pstf/features/RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY/acceptance_criteria.json` — passed.
- `python3 -m json.tool pstf/features/RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY/test_matrix.json` — passed.

## Out of scope

- Generic relay-pool AUTH metadata remains owned by `bahia-8epx.16`; no `internal/adapters/nostr` AUTH metadata paths were changed.
- Stale docs/PSTF wording tracked by `bahia-8epx.13` and `bahia-8epx.14` remains with the concurrent agents.
- NIP-34 repository relay routing remains owned by `bahia-8epx.8`; no `web/` repository files were changed.
- NIP-86 relay administration remains owned by `bahia-8epx.10`.
