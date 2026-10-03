# Phase 5 C1 config-fabric CLI — verification

Issue: `bahia-irsry.13.8`. Branch: `feat/irsry-p5-c1-config`.

## Acceptance evidence

| Criterion | Evidence |
| --- | --- |
| CLI publishes operator-signed canonical config directly to relays | `TestConfigCLIProducesOperatorSignedEventsAndRollsBackFromOutbox` executes the CLI command against an in-process relay-sidecar, checks the operator signature and the CLI outbox's per-relay accepted OK, and observes an applied status from the running consumer. |
| Rollback republishes an older local version as a new replaceable head | The same test publishes versions 1 and 2, rolls back the version-1 event retained by the CLI outbox, and checks version 3, restored list content, and strictly increasing NIP-01 timestamps. |
| Local drift matches the REST projection | `TestConfigFabricPublishApplyStatusClearsDriftEndToEnd` compares the local event projection to the service's REST-backed projection. `TestLocalDriftRetainsAppliedVersionWhenLatestStatusIsAccepted` covers the stable-v3 status coordinate while a newer desired version is pending. |
| Config operator authorization is scoped | `TestFleetOperatorConfigAuthorPassesRelayGateAndConsumer` verifies a configured fleet operator passes both relay admission and consumer trust while an unknown author fails. `TestConfigStatusTimestampOrdersDifferentTrustedOperators` checks status replacement ordering across authors sharing one status address. |
| REST writes removed, read retained | `TestConfigFabricWriteRoutesRemoved` asserts both POST routes are 404. The GET drift route remains wired; the three legacy client methods are deprecated and unused by the CLI. |

## Event contract and finding

The running relay-sidecar consumer accepts NIP-51 kind 30000 membership lists and
NIP-78 kind 30078 policies, using `d=service:<service>:<policy>`, `service`,
`scope`, `version`, and `schema`. This differs from the old Phase 5 sketch of a
30900 config intent. The consumer trusted explicit `config_trusted_pubkeys` but
the relay's admission policy could still reject those authors. C1 also admits
`nostr.authorized_pubkeys` (the TrustSet fleet-ops source) to both layers for
the two config kinds only. Statuses remain service-signed 30900 schema-v3
records with the stable `config-status:<service>:<policy>:<scope>` address.

Rapid versions and multiple operators sharing that status address exposed an
equal-second NIP-01 collision. Status timestamp reservations now persist per
status address, and each latest status carries the last effective config so a
relay-only drift reader does not lose applied truth during activation.

## Gate (2026-10-03)

- `CGO_ENABLED=0 go build ./...` — pass
- `CGO_ENABLED=0 go vet ./...` — pass
- `CGO_ENABLED=0 go test ./...` — pass (includes `internal/archtest` `TestNoNew`, zero added violations)
- `gofmt` on touched Go files and `git diff --check` — pass

Beads status was not changed: `bd show bahia-irsry.13.8` could not connect to
the worktree's Dolt database (`beads_bahia` absent on the local server), and
the slice forbids writing `.beads/`.
