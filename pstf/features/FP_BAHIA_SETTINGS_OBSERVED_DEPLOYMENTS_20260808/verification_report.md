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

Accepted as residual: target-scan coordinates are never tombstoned, because there is no target-removal lifecycle; and the hydration helper duplicates the DNS one, left as is to avoid touching DNS code. *(The target-scan residual was closed by the background adoption scan follow-up below.)*

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

## Follow-up: background adoption scans (D5, 2026-09-28)

Branch `task/bahia-background-adoption-scans-20260928` implements the owner decision recorded in `hitl_decisions.md` D5.

### Implemented behavior

- **Runner.** `adoption-background-scan` is a supervised, non-required tier-3 runner. It runs the same read-only `AdoptionService.Scan` (origin `background`) for one target per call. Bounds:
  - interval 5 m (1 m–24 h);
  - jitter 30 s (≤ interval/2);
  - per-target timeout 1 m;
  - concurrency 2 (≤ 8);
  - exponential backoff `interval × 2^n` capped at 1 h, reset on success.

  One cycle runs at a time; an overlapping trigger is skipped, never queued. An injectable clock keeps the tests sleep-free.
- **Default and scope.** On by default only where `adoption.enabled=true`; `adoption.background_scan.enabled=false` opts out. `true` without adoption is a config error. It needs a publishing projector.
  - Targets default to every `runtime.endpoints` alias, placed in the single environment that references it, else the alias. They can be narrowed with `adoption.background_scan.targets`.
  - Raw `docker_host` targets are never scanned.
  - Derived aliases that cannot be scanned, or that collide after normalization, are skipped with a health warning. Explicit targets must resolve.
- **Scan correctness.** A scan whose context ends after discovery fails and publishes nothing, because lookups would otherwise misclassify adopted workloads. The completion event is published without the scan's deadline, so projection never inherits the scan timeout.
- **Public output.** Results feed only the D1 aggregate.
  - Aggregates republish only on a material change (counts, state, aliases, freshness budget, origin), or as a heartbeat once per repair interval.
  - Late, older results are ignored.
  - Gating state is hydrated from retained events by `entity=runtime-target-scan`, so a restart does not republish.
  - Hydration failures keep live state. A malformed retained `scanned_at` falls back to the event's `created_at`, and the fallback is logged.
  - The background budget is `stale_after_seconds` = repair interval + 2 × (interval + jitter + timeout), 1380 s by default.
  - Everything goes through the existing `publishSigned` dedupe, coalescing, and backoff path.
- **Retirement.** In every mode, the projector repair pass (startup and each repair interval) tombstones:
  - background-origin coordinates outside the scope;
  - coordinates whose `endpoint_ref` left `runtime.endpoints`;
  - with background scanning on, stale operator ad-hoc coordinates.

  The web store already honours target-scan tombstones, and a test now proves it.
- **Read-only.** Docker calls are `GET` containers list, container inspect, and image inspect only. No Bahia state is written, and adoption import is unreachable from the runner. Access and least-privilege notes are in `docs/adoption-production-rollout.md#background-adoption-scans`.
- **Health.** The runner appears in the runner summary. The `adoption_background_scan` readiness check reports counts, skipped aliases, and a per-target outcome code. It returns `warn` on failing or skipped targets, never `fail`. Raw errors are logged only.

### Requirement → evidence

| Requirement | Evidence |
| --- | --- |
| Periodic scan updates aggregates | `TestBackgroundScanRunnerScansEachConfiguredTargetPeriodically`, `TestBackgroundTargetScanRepublishesOnlyMaterialChangesAndFreshnessHeartbeats` |
| Unchanged results don't republish | same + `TestBackgroundTargetScanSuppressesUnchangedCountsAcrossRestart`, `TestBackgroundScanRunnerPublishesOnlyRedactedAggregates` (second unchanged scan) |
| Error backoff | `TestBackgroundScanRunnerBacksOffFailingTargetsAndRecovers` |
| Timeout | `TestBackgroundScanRunnerTimesOutHungTargetWithoutBlockingOthers`, `TestAdoptionScanCancelledAfterDiscoveryPublishesNothing`, `TestAdoptionScanPublishesCompletionWithoutTheScanDeadline` (last two mutation-verified) |
| No overlapping runs | `TestBackgroundScanRunnerNeverOverlapsCyclesAndBoundsConcurrency` |
| Target removal tombstones | `TestRuntimeTargetScanRetirementTombstonesRemovedTargetsAcrossRestart`, `TestRuntimeTargetScanRetirementRunsWithoutBackgroundScanning`, web `removes a retired target scan…` |
| Disabled config does nothing | `TestAdoptionBackgroundScanRunnerIsNotBuiltWhenDisabled`, `TestAdoptionBackgroundScanDefaultsFollowAdoption`, `TestAdoptionBackgroundScanValidation`, `TestRuntimeTargetScanRetirementIsNoopWhenProjectorDisabled` |
| No per-instance detail in any public event | `TestBackgroundScanRunnerPublishesOnlyRedactedAggregates`, `TestRuntimeTargetScanPublishesOnlyRedactedAggregates` |
| Read-only scanning | `TestBackgroundScanIsReadOnlyAndPublishesOnlyAggregates` (Docker double rejects non-GET; no services created) |
| Health/readiness | `TestAdoptionBackgroundScanHealthWarnsOnFailingTargets`, `TestAdoptionBackgroundScanRunnerSkipsCollidingDerivedAliasesButRejectsExplicitOnes` |

### Independent review

A branch-diff review (`code-review`, high) of the first commit raised 10 findings.

Fixed:
- a timeout after discovery no longer publishes miscounted aggregates;
- projection no longer inherits the scan deadline through the in-process bus;
- derived aliases no longer block startup, and skipped aliases are no longer silent;
- retirement moved from the runner into the projector repair pass, so it also covers disabled mode and removed endpoints;
- a swallowed `scanned_at` parse error now falls back to `created_at` with a log line;
- a hydration failure no longer wipes live state;
- the stale budget now covers a cycle waiting a full timeout.

Accepted as residual:
- The target-scan hydration window is 10,000 records per entity tag. Heartbeats produce about 144 events per coordinate per day. A coordinate whose tombstone publish keeps failing long enough to fall outside the window is forgotten after a restart.
- A tombstone and a republish of the same coordinate within one second resolve by NIP-01 lowest id. This is unchanged projector-wide behaviour, and retirement and republish of the same coordinate are minutes apart in practice.

### Quality gates (2026-09-28)

- `go build ./...`, `go vet ./...`: pass.
- `go test ./...`: pass.
- `go test -race ./internal/service ./internal/adapters/nostr ./internal/app ./internal/config ./internal/events`: pass. New tests stressed with `-race -count=20`: pass.
- `web`: `vitest run` 102 files / 857 tests pass.

### Live steps remaining

1. On edge-01 with adoption enabled and `runtime.endpoints` configured, confirm the startup log `background adoption scans enabled`, and that `/health/ready` shows `adoption_background_scan` with `ok` per target after the first cycle.
2. `nak req -k 30900 -a <svc> --tag domain=deployment-inventory <relay>`: confirm each target's `runtime-target-scan` has `origin=background`, `stale_after_seconds=1380`, and a `scanned_at` that advances about every 10 minutes while counts are unchanged, with no more frequent republishes.
3. Start and stop a stray container on the endpoint. Confirm the unmanaged count changes within one interval (5 m ± jitter), and that no container identity appears in any public event.
4. With Docker socket or API audit logging (or an authz proxy) on the endpoint, confirm only the three `GET` routes are called by the Bahia credential during background cycles.
5. Remove an endpoint alias (or set `adoption.background_scan.enabled: false`) and restart. Confirm the corresponding target-scan coordinates are tombstoned, and the Settings row disappears.
6. Make an endpoint unreachable. Confirm `scan_state=unavailable`, a `warn` readiness check, and backoff growth in the logs.
