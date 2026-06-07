# Relay Strategy Final Review — 2026-06-07

Reviewed epic: `bahia-8epx.12`  
Plan: `docs/plans/relay-strategy-2026-06-06.md`

## Scope

Final review only. I reviewed implementation epics `bahia-8epx.1` through `bahia-8epx.11` against the relay-strategy plan, with focus on delivered scope, tests, PSTF/docs, and Nostr semantics. I did not implement broad fixes.

## Lightweight checks

- `go test ./internal/config ./internal/adapters/nostr ./pkg/client ./cmd/cli ./internal/adapters/nostr/relayadmin ./internal/soulfactory` — pass.
- `cd web && npm test -- --run tests/unit/discovery-store.test.js tests/unit/encrypted-controlplane.test.js tests/unit/nostr-client-parsing.test.js tests/unit/controlplane-requests.test.js tests/unit/test-utils-and-fixtures.test.js tests/unit/repositories-store.test.js` — pass, 6 files / 92 tests.

## Review results by child task

| Review task | Implementation epic | Result | Evidence / notes |
| --- | --- | --- | --- |
| `bahia-8epx.12.1` | `bahia-8epx.1` taxonomy/docs/FIPS | Pass with tracked doc cleanup | Current control-plane, event-guide, protocol, FIPS, and PSTF wording establish ContextVM `11316`-`11320` + NIP-51 `30002` as canonical and `31974` as historical. Gap: old design doc still describes request-domain/encrypted traffic as `bahia-browser-v1`; tracked in `bahia-8epx.13`. |
| `bahia-8epx.12.2` | `bahia-8epx.2` backend relay policy sources | Pass | `internal/config/config.go` separates `service_relays`, `browser_relays`, `contextvm_relays`; ContextVM fallback is browser-only compatibility; relay auth-unavailable policy is fixed to `exclude_and_fail`; config tests/PSTF exist. |
| `bahia-8epx.12.3` | `bahia-8epx.3` canonical NIP-51 sets | Pass with tracked PSTF cleanup | Projector publishes signed `bahia-browser-v1`, `bahia-contextvm-v1`, `bahia-service-v1`; tests cover distinct sources, signer pubkey, sidecar fail-closed, publish rejection/zero accepted. Gap: NIP-51 PSTF report has stale wording around NIP-65 scope; tracked in `bahia-8epx.14`. |
| `bahia-8epx.12.4` | `bahia-8epx.4` browser ContextVM discovery | Pass | Browser discovery normalizes `bahia-contextvm-v1`, falls back to browser relays with degraded metadata, preserves fail-closed missing browser set, and uses EOSE query semantics; discovery/encrypted-controlplane tests and PSTF exist. |
| `bahia-8epx.12.5` | `bahia-8epx.5` advisory service NIP-65 | Pass | Projector publishes service-authored kind `10002`; ContextVM relays are `read`, service relays are `write`, browser relays excluded; zero-accepted publish failure is tested; docs mark `10002` advisory only. |
| `bahia-8epx.12.6` | `bahia-8epx.6` operator discovery fallback | Pass with tracked policy cleanup | CLI/env precedence is preserved; trusted bootstrap discovery requires bootstrap relays plus trusted pubkeys; subscription is scoped to trusted `30002` `#d=[bahia-contextvm-v1,bahia-browser-v1]` and chooses after EOSE. Gap/caution: timeout and multi-trusted-pubkey conflict semantics need explicit documentation/tests; tracked in `bahia-8epx.15`. |
| `bahia-8epx.12.7` | `bahia-8epx.7` AUTH/unavailable handling | Pass with tracked metadata cleanup | Browser, backend publish, operator, and FIPS paths handle AUTH/CLOSED/OK rejection without fallback mutation paths; targeted tests/PSTF exist. Gap: generic backend relay-pool subscribe paths should normalize AUTH-unavailable health metadata consistently; tracked in `bahia-8epx.16`. |
| `bahia-8epx.12.8` | `bahia-8epx.8` NIP-34 repository relays | Pass | `30617` `relays` tags are preserved as `relayUrls`; `30618` branch/state lookup passes repository relays before global fallback; missing hints and incomplete EOSE expose degraded metadata; tests/PSTF/docs exist. |
| `bahia-8epx.12.9` | `bahia-8epx.9` SoulFactory/OpenClaw vs ngit | Pass | OpenClaw relays are runtime/control relays; `NgitRelays` are required separately, no fallback to OpenClaw; all ngit relays are passed as repeated `--relay`; tests and `ngit init --help` evidence exist. |
| `bahia-8epx.12.10` | `bahia-8epx.10` optional NIP-86 admin | Pass | NIP-86 admin is disabled by default, target-authorized, secret-ref based, payload-bound NIP-98 signed, and rejects ContextVM/app mutation methods before HTTP; docs/PSTF/tests support the boundary. |
| `bahia-8epx.12.11` | `bahia-8epx.11` verification/docs/follow-ups | Pass | Cross-slice PSTF verification maps items 1–10, user/protocol docs were updated, and optional NIP-11/NIP-66/DM work is already tracked as `bahia-8epx.11.5`, `.11.6`, `.11.7`. |

## Follow-up Beads created

- `bahia-8epx.13` — Fix stale system-discovery relay-set design wording.
- `bahia-8epx.14` — Align NIP-51 PSTF report with NIP-65 projector evidence.
- `bahia-8epx.15` — Clarify operator bootstrap discovery timeout and multi-trust semantics.
- `bahia-8epx.16` — Normalize generic relay-pool AUTH-unavailable metadata.

Existing optional follow-ups remain valid:

- `bahia-8epx.11.5` — Plan advisory NIP-11 relay metadata probes.
- `bahia-8epx.11.6` — Plan configured-trust NIP-66 relay monitor ingestion.
- `bahia-8epx.11.7` — Plan NIP-51 `10050` DM relay-list support for DM-enabled features.

## Nostr semantics assessment

The delivered slices preserve the intended Nostr-native model: scoped subscriptions, EVENT/EOSE-driven historical completion, OK publish verification, CLOSED/AUTH handling, trusted authors, signed event validation in operator discovery, replaceable/latest event semantics, repository-specific NIP-34 routing, and no new relay routing kinds. The review gaps are cleanup/clarification or metadata-consistency follow-ups, not broad replacement of the implemented relay strategy.

## Closure recommendation

Close review tasks `bahia-8epx.12.1` through `bahia-8epx.12.11` and close `bahia-8epx.12`, because each review passed or has a concrete follow-up Bead under `bahia-8epx`.

Do **not** close umbrella `bahia-8epx` yet. The final review found remaining tracked follow-ups under the umbrella (`bahia-8epx.13` through `.16`, plus optional `.11.5` through `.11.7`).
