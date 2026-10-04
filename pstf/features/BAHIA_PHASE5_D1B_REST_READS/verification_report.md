# Bahia Phase 5 D1b REST-read removal verification

Issue: `bahia-irsry.13.15`. The READ route table in `internal/api/router/router.go` no longer mounts the migrated compatibility reads. This slice is integrated with the parallel F3 removal of CLI `--http-fallback`; the unmerged F3 branch is the only remaining HTTP consumer of the fallback-only reads in this branch.

| Acceptance criterion | Verification |
|---|---|
| Migrated GET routes return 404, including paths with surviving write methods | `TestPhase5D1DeletedReadsReturn404`; `TestPhase5D1bDeletedToolReadsAre404WithWritesMounted` |
| HTTP-native reads still work | `TestPhase5D1bRetainedHTTPNativeReadsReturn200`; `TestPhase5D1bBlossomBlobFetchRemains200`; existing router log/payment/config-fabric tests |
| No read-only handler or newly test-only export remains | Removed read-only handler files and read methods from mixed handlers; `TestNoNewTestOnlyExports` passes; `unwired_exports.baseline` drops `TenantHandler.AcceptInvite` |
| User-facing API references reflect removal | Updated `docs/user-guide/` and `docs/soul-factory.md` |

## Caller evidence and disposition

The pre-deletion search covered `cmd/`, `pkg/`, `web/`, `deploy/`, `docs/`, and `internal/adapters/` for the target route strings and their API call sites. No target GET route had a live non-fallback HTTP caller. Representative replacement paths:

| Removed REST reads | Replacement / caller evidence |
|---|---|
| Managed-instance health list/detail/events/recovery attempts; route-canary list/detail/events | `web/src/routes/instance-health/+page.svelte` and `web/src/routes/route-canaries/+page.svelte` import `web/src/lib/stores/operational-views.js`, whose queries read scoped relay state/audit events. |
| SoulFactory runtimes; runtime releases | Soul UI uses `web/src/lib/stores/souls.js` and runtime capability events; release bindings are projected as signed state. No live HTTP caller appeared in the searched consumer trees. |
| Blossom list/servers/health/stats | `web/src/routes/artifacts/+page.svelte` reads `blossomAdminSnapshot` from `operational-views.js`. The only browser Blossom HTTP fetch is blob content in `web/src/lib/components/SBOMDetails.svelte`. |
| SBOM, signatures, tool provisioning, notification log | Artifact pages use relay SBOM collections and encrypted signature operations; `web/src/lib/stores/notifications.svelte.js` uses encrypted Nostr for log reads. No live HTTP GET caller appeared in the searched consumer trees. |
| Service, environment, state-list, policy, worker, build, artifact, org, secret, notification-channel reads | Remaining `pkg/client/client.go`, `pkg/client/registry_http_reads.go`, and `pkg/client/notification_http.go` HTTP calls are the CLI `--http-fallback` path being deleted by F3. `cmd/cli/nostr_reads.go`, `cmd/cli/nostr_confidential_reads.go`, and `cmd/cli/nostr_worker_build_artifact_reads.go` contain the fallback dispatch. |

Retained HTTP reads: root `/health`, `/ready`, optional `/metrics`; stored/live logs (`/api/v1/deployments/runs/{id}/logs`, `/api/v1/services/{id}/environments/{envId}/logs`); `/api/v1/blossom/blob/{hash}` (live browser fetch at `web/src/lib/components/SBOMDetails.svelte:142`); payment cost/history, config-fabric drift, and virtualization reads (outside this deletion scope). No target GET route was retained for a non-fallback caller.

## Gate and handoff

Passed on this worktree: `git diff --check`, gofmt check of changed Go files, `CGO_ENABLED=0 go build ./...`, `CGO_ENABLED=0 go vet ./...`, and `CGO_ENABLED=0 go test ./...`. Beads claim/close was unavailable: `bd show bahia-irsry.13.15` reported missing Dolt database `beads_bahia`; `.beads/` was not modified. The F3 CLI fallback removal must be integrated with this branch before release, so the unmerged fallback calls do not hit 404s.
