# Bucket 3: Tool Provisioning Recovery and Serialization Verification

## Scope

Beads: `bahia-ph2r`, `bahia-h6st`.

Implemented only the tool provisioning coordinator/app wiring path:

- `internal/service/tool_provisioning_coordinator.go`
- `internal/service/tool_provisioning_coordinator_test.go`
- `internal/app/app.go`

## Intended behavior

1. Newly-arrived kind 5976 tool provisioning requests remain event-driven through the control-plane reactor.
2. Stored stranded intents are recovered explicitly by a background runner on startup/resume and then periodically as a durable recovery pass.
3. Recovery scans only persisted non-terminal provisioning states: `pending`, `validating`, `approved`, `building`, `deploying`, `observing`.
4. In-process duplicate work for the same intent is collapsed by intent ID.
5. Provisioning pipelines are serialized per `(service_id, environment_id)` while different targets can still proceed concurrently.

## Acceptance criteria mapping

| Bead | Acceptance criterion | Verification |
| --- | --- | --- |
| bahia-ph2r | Newly-arrived requests are event-triggered; recovery is explicit and documented. | Coordinator `Run` comment documents stored-intent-only recovery; reactor test package still passes. |
| bahia-ph2r | Stranded `pending`/`approved` intents are retried on startup/resume. | `TestToolProvisioningRunRecoversApprovedIntentImmediately`; `TestToolProvisioningRecoveryRetriesPendingIntent`. |
| bahia-ph2r | Recovery tests do not use sleeps or ticker timing. | Tests call recovery directly or trigger immediate `Run` pass with channel handshakes; ticker interval set to `time.Hour`. |
| bahia-h6st | Two intents for same service/environment cannot deploy/update profile state concurrently. | `TestToolProvisioningSerializesSameTargetAndMaintainsPreviousImage`. |
| bahia-h6st | Intents for different targets can still run independently. | `TestToolProvisioningAllowsDifferentTargetsConcurrently`. |
| bahia-h6st | Tests prove `PreviousImageDigest`/`CurrentImageDigest` remains consistent under concurrent requests. | Same-target test asserts final `PreviousImageDigest` equals first image and `CurrentImageDigest` equals second image. |
| bahia-h6st | Recovery should not let older same-target stranded intents overwrite newer ones. | Oracle review identified this edge case; `TestToolProvisioningRecoveryOrdersSameTargetOldestFirst` verifies unordered repository listings are recovered oldest-first so the newest intent remains final. |

## Verification commands

```bash
go test ./internal/service ./internal/app ./internal/controlplane
```

Result: PASS

```text
ok  github.com/openagentsinc/bahia/internal/service
ok  github.com/openagentsinc/bahia/internal/app
ok  github.com/openagentsinc/bahia/internal/controlplane
```

## Review follow-up

A scoped Oracle review flagged that unordered durable recovery could process a newer same-target intent before an older one, then let the older intent overwrite final profile state. The implementation now sorts recovery work by target and `CreatedAt` oldest-first, with a regression test proving the newest same-target intent remains final even when the repository lists it first.

## Notes

No commits, staging, or pushes were performed per user instruction. The pre-existing `coverage-summary.json` modification was not touched.
