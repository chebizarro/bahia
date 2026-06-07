# Verification Report: bahia-tem1

Date: 2026-06-07

Bead: `bahia-tem1`

## Decision

Bahia ships **no trusted NIP-66 monitor pubkeys by default**.

No monitor pubkeys are documented as built-in defaults because repository evidence did not identify explicit operator-approved trust anchors. Operators may configure `nostr.trusted_relay_monitor_pubkeys` explicitly. When configured, NIP-66 events remain advisory health/capability metadata only.

## Advisory criteria

Configured NIP-66 monitor data may annotate relay health, capability warnings, or operator-facing ranking only. It must not:

- establish Bahia service trust,
- override configured trusted service pubkeys,
- override configured relay policy,
- remove all configured relays, or
- replace NIP-11/NIP-42/EOSE/OK/CLOSED handling.

## Evidence

- `docs/plans/relay-strategy-2026-06-06.md` states the safe default is no trusted monitors and that NIP-11/NIP-66 are advisory only.
- `docs/user-guide/nostr-integration.md` documents explicit `trusted_relay_monitor_pubkeys` configuration and says monitor trust is empty by default.
- `docs/nostr-event-implementation-guide.md`, `docs/nostr-commands.md`, and `docs/protocol-compatibility.md` describe NIP-66 as advisory metadata, not a trust root.
- `internal/config/config.go` exposes `TrustedRelayMonitorPubkeys` as configuration and validation, with no built-in non-empty default found in the inspected repository evidence.
- `pstf/features/bahia-8epx.11` maps configured-trust NIP-66 ingestion tests and explicitly excludes default trust in NIP-66 monitors.

## Verification

```bash
python3 -m json.tool pstf/features/bahia-tem1/feature_spec.json >/tmp/bahia-tem1.feature_spec.json
python3 -m json.tool pstf/features/bahia-tem1/acceptance_criteria.json >/tmp/bahia-tem1.acceptance_criteria.json
python3 -m json.tool pstf/features/bahia-tem1/test_matrix.json >/tmp/bahia-tem1.test_matrix.json
```

Result: pass.

## Result

Acceptance criteria are satisfied. Trusted NIP-66 monitor defaults are empty; no pubkeys, trust rationale for non-empty defaults, or dependent implementation Bead are required.
