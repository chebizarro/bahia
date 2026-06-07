# Relay Strategy NIP-51 Sets Verification

Beads: `bahia-8epx.3`, `bahia-8epx.3.1`, `bahia-8epx.3.2`, `bahia-8epx.3.3`

## Scope

This slice verifies backend discovery projection for service-authored NIP-51 kind `30002` relay sets only. Advisory service NIP-65 kind `10002` projection is verified separately in `pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS` / `bahia-8epx.5`; user/operator-authored NIP-65 preferences are not implemented or claimed by this report. Browser discovery, CLI fallback discovery, AUTH behavior changes, NIP-86, and final docs-only verification remain outside this slice.

## Evidence

Targeted checks run on 2026-06-07:

```bash
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP51_SETS/feature_spec.json >/tmp/relay_strategy_nip51_feature_spec.check
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP51_SETS/acceptance_criteria.json >/tmp/relay_strategy_nip51_acceptance.check
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP51_SETS/test_matrix.json >/tmp/relay_strategy_nip51_test_matrix.check
go test ./internal/config ./internal/adapters/nostr
# ok   github.com/openagentsinc/bahia/internal/config (cached)
# ok   github.com/openagentsinc/bahia/internal/adapters/nostr 0.338s
```

## Verified behavior

- `bahia-browser-v1` is projected from `nostr.browser_relays` / `BrowserRelayPolicyRelays()`.
- `bahia-contextvm-v1` is projected from `nostr.contextvm_relays` / `ContextVMRelayPolicyRelays()`.
- `bahia-service-v1` is projected from `nostr.service_relays` / `ServiceRelayPolicyRelays()`.
- All three relay sets are signed by the configured service key and published as NIP-51 `30002` events.
- Sidecar-enabled discovery fails before publishing when public browser relay policy is absent.
- Relay-set publisher errors are returned through `RepublishSnapshot`, preserving the existing RelayPool OK/partial-failure surfacing path instead of swallowing discovery projection failures.
- A publish result with zero relay acceptances is treated as a discovery projection failure.

## Remaining work outside this slice

Downstream Beads continue to own browser discovery normalization (`bahia-8epx.4`), service-authored advisory NIP-65 verification (`bahia-8epx.5` / `pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS`), CLI fallback (`bahia-8epx.6`), AUTH behavior (`bahia-8epx.7`), NIP-86 (`bahia-8epx.10`), and final documentation/verification (`bahia-8epx.11`).
