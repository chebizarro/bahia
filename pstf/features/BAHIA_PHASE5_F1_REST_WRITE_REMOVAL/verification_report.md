# Bahia Phase 5 F1 REST write removal

Bead: `bahia-irsry.13.16`. Source: `docs/designs/phase5-cli-client.md`, Wave 6 F1. Caller search covered `cmd/`, `pkg/`, `web/`, `deploy/`, `docs/`, `internal/adapters/`, and `scripts/`; test fixtures and stale API reference rows alone were not treated as live callers.

| Decision | Routes | Evidence |
|---|---|---|
| Delete | `POST /repositories/ci/lookup`, `POST /blossom/list` | No non-test client call remains; direct Blossom listing exists in `internal/adapters/blossom/list.go` and relay operational views cover browser inventory. |
| Delete | `POST /builds`, `PATCH /builds/{id}/status` | No live HTTP caller found. Browser build result registration uses `web/src/lib/stores/arcana-build.js` and CLI uses `cmd/cli/contextvm_commands.go`; build-registry intent/daemon path is owned by `bahia-irsry.77`. |
| Delete | Four `POST /ml/*` routes, `PUT /llm/routes/{id}` | No live HTTP caller found; web ML/LLM controls use Nostr request stores. `bahia-irsry.76` owns LLM intent handling. |
| Delete | `POST /deployments/runs`, `POST /deployments/runs/{id}/complete` | No non-test HTTP caller found; runtime and registry own run lifecycle internally. |
| Delete | `POST /payments/estimate` | No HTTP caller found. Web computes estimates in `web/src/routes/services/deploy-cost-estimate.js`; MCP estimates from relay-backed worker pricing in `internal/mcp/store_read_tools.go`. The now test-only service estimator was removed. |
| Delete | `POST /artifacts/{id}/signatures/verify`, `POST /notifications/channels/{id}/test` | Browser calls encrypted request operations in `web/src/lib/stores/artifact-signatures.svelte.js` and `notifications.svelte.js`. |
| Delete | `POST /tools/denylist`, `DELETE /tools/denylist/{package}/{manager}` | No live HTTP caller found in searched roots. |
| Delete | `POST /api/v1/mcp` | Native `/mcp` remains mounted and is the documented HTTP MCP boundary. |
| Retain | `POST`/`DELETE` managed-instance maintenance | Direct browser caller: `web/src/routes/instance-health/+page.svelte:82,98`; no intent replacement yet. |
| Retain | `POST /artifacts/{id}/sbom` | Documented curl import in `docs/user-guide/features/artifacts.md:136`; `docs/designs/sbom-real-support.md:322` explicitly keeps the large-document import boundary. |
| Retain | `POST /soulfactory/legacy-reconciliation/preview` and `/apply` | One-time signed migration workflow in `docs/user-guide/features/souls.md:347-348`; no intent replacement yet. |

Seventeen deleted method/path pairs are covered by `TestPhase5F1DeletedWriteRoutesReturn404`; five retained method/path pairs are covered by `TestPhase5F1RetainedWriteBoundariesStayMounted`. Existing `/mcp` auth tests prove its native endpoint still works. The `unwired_exports` baseline shrank by one (`NewLogHandler` test-only wrapper); no baseline entries were added.

The Beads Dolt server currently does not expose `beads_bahia` at `127.0.0.1:61524`, so this worktree cannot claim/close the bead or file a follow-up without writing `.beads/`. Retained route replacement ownership remains for orchestration handoff; the F1 annotations reference the final migration cleanup bead but do not imply replacements are implemented in this slice.

## Final gate

- `CGO_ENABLED=0 go build ./...` — passed.
- `CGO_ENABLED=0 go vet ./...` — passed.
- `CGO_ENABLED=0 go test ./...` — passed, including `internal/archtest` and the F1 router tests.
- `gofmt -l` on changed Go files — empty; `git diff --check` — passed.

## Integration reconciliation with D1b

The merged router keeps D1b's 75 deleted-read method/path checks and F1's 17 deleted-write checks, without duplicate rows in those tables. Retained health/ready, metrics, Blossom blob fetch, payment/config-fabric reads, virtualization authorization, logs, managed-instance maintenance, SBOM ingress, Soul reconciliation, and native `/mcp` retain route or handler behavior tests. `CGO_ENABLED=0 go build ./...`, `go vet ./...`, and `go test ./...` passed on the integration branch. The focused API + architecture gate passed 5 packages, 129 top-level tests, and 156 subtests.
