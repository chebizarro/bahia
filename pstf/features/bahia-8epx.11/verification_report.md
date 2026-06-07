# Verification Report: bahia-8epx.11

Date: 2026-06-07

Beads: `bahia-8epx.11`, `bahia-8epx.11.1`, `bahia-8epx.11.2`, `bahia-8epx.11.3`, `bahia-8epx.11.4`, `bahia-8epx.11.5`, `bahia-8epx.11.6`, `bahia-8epx.11.7`

## Scope

This report covers relay strategy plan Items 10 and 11 only: cross-slice PSTF verification, user-facing/protocol documentation, bounded optional NIP-11/NIP-66 planning, and the explicit NIP-51 kind `10050` notification DM relay-list slice for `bahia-8epx.11.7`. Production implementation epics `bahia-8epx.1` through `bahia-8epx.10` were not reopened.

## Cross-slice evidence map

| Plan / Bead area | PSTF evidence | Verification focus |
| --- | --- | --- |
| Item 1 / `bahia-8epx.1` bootstrap wording | `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` | ContextVM discovery `11316`-`11320` plus NIP-51 `30002`, EOSE-bounded bootstrap, fail-closed browser behavior, legacy `31974` migration-only wording. |
| Item 2 / `bahia-8epx.2` relay policy sources | `pstf/features/RELAY_POLICY_SOURCES` | Independent `service_relays`, `browser_relays`, `contextvm_relays`, compatibility alias handling, `relay_auth_unavailable=exclude_and_fail`. |
| Item 3 / `bahia-8epx.3` NIP-51 sets | `pstf/features/RELAY_STRATEGY_NIP51_SETS` | Service-authored `bahia-browser-v1`, `bahia-contextvm-v1`, `bahia-service-v1`, sidecar fail-closed behavior, OK/zero-accepted publish failures. |
| Item 4 / `bahia-8epx.4` browser discovery | `pstf/features/RELAY_STRATEGY_CONTEXTVM_BROWSER_DISCOVERY` | ContextVM relay normalization, degraded browser fallback, encrypted capability gating, EVENT/EOSE/CLOSED/AUTH callbacks. |
| Item 5 / `bahia-8epx.5` service NIP-65 | `pstf/features/RELAY_STRATEGY_NIP65_SERVICE_PREFS` | Advisory service `10002`, ContextVM read and service write markers, zero-accepted publish failure, docs advisory boundary. |
| Item 6 / `bahia-8epx.6` operator fallback | `pstf/features/RELAY_STRATEGY_OPERATOR_BOOTSTRAP_DISCOVERY` | CLI/env precedence, trusted bootstrap discovery, ContextVM relay preference, EOSE and failure paths. |
| Item 7 / `bahia-8epx.7` AUTH/CLOSED | `pstf/features/bahia-8epx.7` | NIP-42 AUTH callbacks, auth-required exclusion, relay health metadata, OK=false reasons, no post-acceptance fallback. |
| Item 8 / `bahia-8epx.8` NIP-34 | `pstf/features/bahia-8epx.8` | `30617` relay hints preserved, `30618` state queries prefer repository relays, degraded fallback and incomplete EOSE metadata. |
| Item 9 / `bahia-8epx.9` SoulFactory/ngit | `pstf/features/bahia-8epx.9` | OpenClaw runtime/control relays remain distinct from required Ngit publication relays; repeated ngit `--relay` support. |
| Item 10 / `bahia-8epx.10` NIP-86 | `pstf/features/bahia-8epx.10` | Disabled-by-default relay-owner HTTP administration, Bahia-owned/authorized target checks, payload-bound NIP-98, ContextVM mutation rejection. |
| Item 10 follow-up / `bahia-8epx.11.4` | `pstf/features/bahia-8epx.11` | NIP-11/NIP-66 advisory metadata bounded by follow-up Beads. |
| DM relay-list slice / `bahia-8epx.11.7` | `pstf/features/bahia-8epx.11` | NIP-51 `10050` remains absent by default, publishes only from explicit `nostr.dm_relay_lists` config for notification DM service identity, and stays separate from browser, ContextVM, and service relay sets. |

## Commands run

```bash
python3 - <<'JSON_VALIDATION'
# JSON-loaded epic-11 plus referenced relay-strategy PSTF JSON files.
JSON_VALIDATION
# Result: pass; 30 PSTF JSON files loaded successfully.

GOCACHE=/tmp/bahia-go-cache go test ./internal/config ./internal/adapters/nostr ./pkg/client ./cmd/cli ./internal/fipsbridge ./internal/adapters/nostr/relayadmin ./internal/soulfactory
# ok github.com/openagentsinc/bahia/internal/config (cached)
# ok github.com/openagentsinc/bahia/internal/adapters/nostr (cached)
# ok github.com/openagentsinc/bahia/pkg/client (cached)
# ok github.com/openagentsinc/bahia/cmd/cli (cached)
# ok github.com/openagentsinc/bahia/internal/fipsbridge (cached)
# ok github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin (cached)
# ok github.com/openagentsinc/bahia/internal/soulfactory 0.358s

ngit init --help
# Result: pass; output includes `--relay <RELAY>...` repeated relay argument support.

cd web && npm test -- --run   tests/unit/discovery-store.test.js   tests/unit/encrypted-controlplane.test.js   tests/unit/nostr-client-parsing.test.js   tests/unit/controlplane-requests.test.js   tests/unit/test-utils-and-fixtures.test.js   tests/unit/repositories-store.test.js
# Test Files 6 passed (6); Tests 92 passed (92)

# bahia-8epx.11.7 focused verification

go test ./internal/config ./internal/adapters/nostr ./internal/kinds
# ok github.com/openagentsinc/bahia/internal/config
# ok github.com/openagentsinc/bahia/internal/adapters/nostr
# ok github.com/openagentsinc/bahia/internal/kinds

cd web && npm test -- --run tests/unit/discovery-store.test.js
# Test Files 1 passed (1); Tests 9 passed (9)

python3 -m json.tool pstf/features/bahia-8epx.11/acceptance_criteria.json >/tmp/bahia-8epx.11-acceptance.json
python3 -m json.tool pstf/features/bahia-8epx.11/test_matrix.json >/tmp/bahia-8epx.11-test-matrix.json
python3 -m json.tool pstf/features/bahia-8epx.11/feature_spec.json >/tmp/bahia-8epx.11-feature-spec.json
# Result: pass
```

## Documentation validation

Targeted RepoPrompt searches confirmed:

- No docs now present `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` or `kind 31974 + NIP-51` as current bootstrap wording.
- User-facing docs now name ContextVM discovery `11316`-`11320` plus NIP-51 `30002` relay sets as canonical bootstrap.
- `docs/user-guide/nostr-integration.md`, `docs/protocol-compatibility.md`, `docs/nostr-commands.md`, and `docs/event-spec.md` document NIP-11/NIP-66 advisory-only metadata and NIP-51 `10050` DM relay-list boundaries, including explicit `notifications.nostr_dm` plus `nostr.dm_relay_lists` opt-in and no inference from browser/ContextVM/service relay sets.
- `docs/user-guide/cli-reference.md` documents CLI relay precedence and no REST fallback after relay acceptance.
- `docs/relay-sidecar.md` documents `service_relays`, `browser_relays`, `contextvm_relays`, and `relay_auth_unavailable` behavior.
- Remaining `5980`/`7980` search hits are historical investigation notes, not user-facing current behavior.

## Follow-up Beads status

The optional metadata/DM work is tracked in Beads rather than comments or prose-only follow-up. These tasks were created during `bahia-8epx.11` verification on 2026-06-07, then parented to the overall relay-strategy epic `bahia-8epx`:

- `bahia-8epx.11.5` — Plan advisory NIP-11 relay metadata probes.
- `bahia-8epx.11.6` — Plan configured-trust NIP-66 relay monitor ingestion.
- `bahia-8epx.11.7` — Implemented in this slice for explicit notification DM service-identity `10050` relay-list publication with safe defaults.

The remaining optional metadata boundaries preserve safe defaults: no NIP-66 trusted monitors by default, advisory metadata cannot establish trust, and optional metadata cannot remove all configured relays. The DM relay-list boundary now preserves safe defaults in code: no `10050` publication by default and no public/browser/ContextVM/service relay inference.

## Advisory metadata implementation evidence for `bahia-8epx.11.5` and `bahia-8epx.11.6`

Completed on 2026-06-07:

- `pkg/discovery/resolver.go` records best-effort NIP-11 advisory metadata for each configured relay. Missing metadata, malformed `supported_nips`, and limiting metadata such as auth/payment/restricted-write/max-limit are visible through `RelayMetadata()` and do not prevent the resolver from connecting to the configured relay set.
- `web/src/lib/stores/dns.svelte.js` preserves browser DNS NIP-11 metadata as advisory relay health and adds configured-trust NIP-66 monitor ingestion. The browser subscribes to kind `10166` and `30166` only when trusted monitor pubkeys are configured, scopes `30166` to configured relay `d` tags, ignores untrusted monitors and unknown relays, and never changes `connection.relays` or `connection.servicePubkey` from metadata.
- `bahia-8epx.11.7` adds explicit notification DM service-identity kind `10050` relay-list publication with no default publication and no inference from browser, ContextVM, or service relay sets.

Additional commands run:

```bash
go test ./pkg/discovery
# ok github.com/openagentsinc/bahia/pkg/discovery 0.279s

cd web && npm test -- --run tests/unit/dns-store-subscriptions.test.js
# Test Files 1 passed (1); Tests 7 passed (7)

python3 -m json.tool pstf/features/bahia-8epx.11/feature_spec.json >/tmp/bahia-8epx.11.feature_spec.json
python3 -m json.tool pstf/features/bahia-8epx.11/acceptance_criteria.json >/tmp/bahia-8epx.11.acceptance_criteria.json
python3 -m json.tool pstf/features/bahia-8epx.11/test_matrix.json >/tmp/bahia-8epx.11.test_matrix.json
# Result: pass
```

## Result

Cross-slice relay strategy PSTF and documentation evidence is complete for `bahia-8epx.11`, including completed advisory NIP-11/NIP-66 slices `bahia-8epx.11.5`/`bahia-8epx.11.6` and explicit DM relay-list slice `bahia-8epx.11.7`. No blocking verification gap was found that requires reopening implementation epics `bahia-8epx.1` through `bahia-8epx.10`.
