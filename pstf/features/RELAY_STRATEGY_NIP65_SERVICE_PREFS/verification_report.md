# Relay Strategy NIP-65 Service Preferences Verification

Beads: `bahia-8epx.5`, `bahia-8epx.5.1`, `bahia-8epx.5.2`, `bahia-8epx.5.3`

## Scope

This slice implements backend projection of the service-authored advisory NIP-65 kind `10002` relay preference event only. Browser discovery normalization, CLI fallback, AUTH behavior changes, NIP-86, relay metadata, and DM relay-list work remain outside this slice.

## Evidence

Targeted checks run on 2026-06-07:

```bash
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS/feature_spec.json >/tmp/relay_strategy_nip65_feature_spec.check
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS/acceptance_criteria.json >/tmp/relay_strategy_nip65_acceptance.check
python3 -m json.tool pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS/test_matrix.json >/tmp/relay_strategy_nip65_test_matrix.check
go test ./internal/adapters/nostr -run 'TestProjectorPublishesSystemDiscoverySnapshot|TestProjectorSystemDiscoveryFailsWhenNIP65RelayPreferencesHaveNoAcceptedRelays|TestProjectorSystemDiscoveryFailsWhenSidecarBrowserRelaysAbsent|TestProjectorSystemDiscoverySurfacesRelaySetPublishFailure|TestProjectorSystemDiscoveryFailsWhenRelaySetHasNoAcceptedRelays'
# ok  	github.com/openagentsinc/bahia/internal/adapters/nostr	0.318s
```

## Verified behavior

- System discovery projection signs and publishes service-authored kind `10002` with the configured Bahia service key.
- The `10002` event uses NIP-65 `r` tags with `read` markers for ContextVM request/read relays from `ContextVMRelayPolicyRelays()`.
- The `10002` event uses NIP-65 `r` tags with `write` markers for service publish/backfill relays from `ServiceRelayPolicyRelays()`.
- Browser-only relays are not projected into the service advisory `10002` event.
- ContextVM discovery plus NIP-51 `30002` relay sets remain the canonical Bahia bootstrap; docs mark service `10002` as advisory only.
- A zero-accepted publish result for kind `10002` is returned as a projection failure instead of being treated as success.

## Remaining work outside this slice

Downstream Beads continue to own browser discovery normalization (`bahia-8epx.4`), CLI fallback (`bahia-8epx.6`), AUTH behavior (`bahia-8epx.7`), NIP-86 (`bahia-8epx.10`), and relay metadata/DM follow-up (`bahia-8epx.11`).
