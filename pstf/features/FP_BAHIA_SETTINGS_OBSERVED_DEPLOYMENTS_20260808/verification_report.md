# FP_BAHIA_SETTINGS_OBSERVED_DEPLOYMENTS_20260808 Verification Report

Remediation of review `review-fp-bahia-settings-observed-deployments-20260927` (revision 3d1a09c0), which found that 18a5079a was a partial discovery/UI projection and not an authoritative deployment-inventory protocol.

## Implemented behavior

- **Protocol.** A signed canonical `30900` deployment inventory (`domain=deployment-inventory`, `schema=bahia.deployment-inventory.v1`); no new kind. Complete per-environment snapshots are built from service state, the latest runtime observation, the desired artifact, the deployment unit, and managed-instance supervisor health. Coverage (`observed`, `desired_only`, `observed_only`, `unknown`), `drift_evaluated`, reconcile failure codes, and a freshness budget are explicit. Deleted environments are tombstoned, including after a restart (hydrated from retained events).
- **Unmanaged (option B).** Adoption scan completion publishes redacted per-target aggregates (counts, scan state, scan time) and never per-instance detail. `adoption/scan` and `adoption/import` require NIP-59 wrapped requests so that per-instance responses are encrypted to the authorized requester.
- **Redaction.** Allowlisted projection. The pre-existing public `runtime.observation` audit, which carried the raw Docker host fallback, container ID, and normalized env/command/volumes, is reduced to an allowlisted summary.
- **Discovery.** Discovery no longer carries `observed_deployments`. Split topology (`nostr.sidecar.enabled=false`) still publishes discovery, relay sets, and the inventory.
- **Web.** A new protocol module and store accept only trusted author + valid id/signature. They apply addressable ordering with the lowest-id tie-break, honor tombstones, reject malformed payloads without clobbering, re-verify cached events (provisional until relay confirmation, flagged unconfirmed after EOSE), and use a retained subscription with backoff resubscribe and idempotent replay. Settings shows **Observed deployments** per environment, and a separate **Build provenance** diagnostics section.

## Independent review

A branch-diff review (`code-review`, high) raised 10 findings. Fixed:
- unsafe instance target names now get a stable alias, so they no longer blank the target and cause the browser to reject the whole snapshot;
- instances carry their deployment unit id and the UI keys them by unit and target;
- coalesced refresh;
- material-change gating with a forced repair refresh;
- reconcile-cycle and instance-health triggers;
- direct tombstone on EnvironmentDeleted and a warning when the hydration window saturates;
- scan `environment_id` grouping;
- allowlists for the runtime action and adoption-import audits;
- the transport-provided `ContextVMRequest.Encrypted` flag.

Accepted as residual: target-scan coordinates are never tombstoned, because there is no target-removal lifecycle; and the hydration helper duplicates the DNS one, left as is to avoid touching DNS code.

## Acceptance mapping

| Criterion | Evidence | Result |
| --- | --- | --- |
| AC1 | `TestDeploymentInventoryPublishesCompleteSignedEnvironmentSnapshots` | Pass |
| AC2 | same + `TestRuntimeTargetScanPublishesOnlyRedactedAggregates` + web staleness/stopped/desired-only tests | Pass |
| AC3 | same + `TestDeploymentInventoryReportsUnsupervisedInstanceCoverage` + web multi-env/instance test | Pass |
| AC4 | `TestDeploymentInventoryRepublishesOnRollbackAndSuppressesUnchanged` + web store reconnect/rollback test | Pass |
| AC5 | `version.test.js`, `settings-section-order.test.js`, split-topology discovery test | Pass |
| AC6 | `TestProjectorPublishesSystemDiscoveryInSplitRelayTopology` | Pass |
| AC7 | `deployment-inventory.test.js` (11 protocol + 3 store tests), `TestDeploymentInventoryTombstonesDeletedEnvironmentsAcrossRestart` | Pass |
| AC8 | `TestDeploymentInventoryRedactsRuntimeDetailFromEveryPublicEvent` (mutation-verified: bypassing the audit allowlist makes it fail), scan aggregate test, web redacted-scan view test | Pass |
| AC9 | `TestAdoptionScanPerInstanceDetailReachesOnlyAuthorizedEncryptedRequester`, `TestOperatorContextVMHandlersScopedAuthorization` | Pass |
| AC10 | Live edge-01 acceptance | **Not run** (live, requires deployment) |

## Quality gates (2026-09-28)

- `go build ./...`, `go vet ./...`: pass.
- `go test ./...`: pass.
- `go test -race ./internal/adapters/nostr ./internal/controlplane ./internal/service ./internal/app`: pass.
- `web`: `vitest run` 102 files / 855 tests pass; `npm run lint` (svelte-check) 0 errors / 0 warnings; `npm run build` pass.

## Live acceptance remaining (AC10)

1. Deploy to edge-01 with `adoption.allowed_pubkeys` and supervision configured, and confirm the backend publishes `30900` `#domain=deployment-inventory` for each environment (`nak req -k 30900 -a <svc> --tag domain=deployment-inventory <relay>`).
2. Compare every rendered row (bahia, bahia-web, bahia-relay, …) against `docker compose ps --format json` and `docker inspect` digests, and against the `runtime_observations` / `environment_service_state` rows.
3. Deploy and then roll back one service; confirm the Settings row changes digest and drift without a web rebuild.
4. Stale: stop the reconciler (or the backend) longer than `stale_after_seconds`; confirm rows are labelled stale.
5. Unmanaged: start a stray container on the target and run `bahia adopt scan --encrypted …`. Confirm the public event shows only counts and the CLI response shows the container. Confirm a plaintext scan is refused.
6. Secrets: grep all `30900`/`4903`/`11316` events from the service pubkey and the rendered Settings DOM for hosts, container IDs, env keys/values, and credentials. Expect none.
