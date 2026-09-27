# Loom deployment-unit dispatch review fixes

Review revision: `c01aaa471746dc6f1ff12f58da4e5ee2b4f763f3526ad9835c4788ee10e3d19f`.

| Criterion | Deterministic evidence |
| --- | --- |
| Missing policy or empty selected worker refuses before publish, with no direct fallback | `TestExecuteDeployment_LoomDispatchUnitRejectsUnresolvedWorkerAndRelayMismatch` (`missing worker policy`, `empty selected worker`) |
| Worker relay overlap is checked against the actual Bahia publish pool and fresh worker advertisement | `TestDispatchRelaysUsesPublishPool`; `TestExecuteDeployment_LoomDispatchUnitRejectsUnresolvedWorkerAndRelayMismatch` (`disjoint relays`, `worker advertises no relays`, `Bahia has no publish relays`); `TestExecuteDeployment_LoomDispatchUnitUsesFreshWorkerRelayAdvertisement` (stale overlap refused, fresh overlap accepted) |
| Valid worker and shared relay retain Loom job, worker, and deployment-unit binding | `TestExecuteDeployment_LoomDispatchUnitSubmitsJobWithoutDirectRuntime`; `TestExecuteDeployment_LoomDispatchUnitUsesFreshWorkerRelayAdvertisement` |
| Direct-runtime default and legacy non-unit workerless behavior remain intact | `TestExecuteDeployment_DockerUnitUsesManagedEndpointWithoutLoom`; `TestExecuteDeployment_MaxComposeUnitRendersFullDesiredState`; `TestExecuteDeployment_WorkerlessLoomPathSkipsDispatchAdmission` |

Local gates in the `loom-dispatch` worktree: `go build ./...`, `go vet ./...`, `go test ./...`, and `go test -race ./internal/workflow ./internal/adapters/loom ./internal/service ./internal/controlplane` all passed. The new focused Loom-unit tests also passed with `-race -count=3`. One oracle review identified the fresh-advertisement counterfactual gap; both directions were added and verified.

This is local source and test evidence only. An operator still needs to confirm a safe target worker advertises a relay also configured in Bahia's live Loom publish pool, then rerun the Bahia service/deploy Firecracker canary and observe the real Loom job, worker status/result, deployment-run terminal projection, and post-run health. The separate Firecracker guest deploy tooling blocker remains outside this review fix.
