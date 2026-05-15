# Verification Report — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Summary

Final verification for Beads issue `bahia-i0rk.3` is **complete** on 2026-05-15.

The prior OpenClaw blocker is resolved: product-valid OpenClaw evidence now uses the Bahia-owned sidecar from `bahia-8ycd.4`, not direct upstream OpenClaw patches. Bahia, the owned OpenClaw sidecar, and Swarmstr/Metiq all have deterministic event-driven integration/contract evidence for capability announcement, draft-backed provisioning, runtime `38384/38386` execution, final `31951/7950` publication, and one lifecycle `1950 -> 7950` action.

This verification is an evidence bundle from deterministic repository tests and static audit, not a new live multi-relay end-to-end run.

## Artifact status

- `feature_spec.json` — `final_verified`
- `acceptance_criteria.json` — `final_verified`
- `test_matrix.json` — updated with final passing Bahia, OpenClaw sidecar, and Metiq evidence
- `defects.json` — updated; no open blockers remain
- `hitl_decisions.md` — updated to record the resolved OpenClaw sidecar decision
- `docs/soulfactory-runtime-control.md` — remains the shared runtime-control contract
- `docs/openclaw-soulfactory-sidecar.md` — records the owned OpenClaw product evidence path

## Acceptance Criteria Status

| AC ID | Status | Basis |
| --- | --- | --- |
| SFRL-AC-001 | Verified | Static audit found no external REST lifecycle-control path in Bahia runtime lifecycle paths, the owned OpenClaw sidecar, or Metiq SoulFactory bridge evidence. The OpenClaw sidecar uses a local command-driver seam and Nostr `30317/38384/38386`, not an upstream patch or REST control API. |
| SFRL-AC-002 | Verified | Bahia relay-bus tests passed. Evidence covers accepted/false `OK`, all-relay reject, EOSE backfill transition, CLOSED/auth-required handling, duplicate EVENT dedupe, reconnect/reissue, and no timeout/relay-close terminal inference. |
| SFRL-AC-003 | Verified | Bahia tests passed for exact `31952` draft capture, spec-hash propagation/mismatch rejection, replay short-circuiting, runtime binding/capability/deploy fields, and final `31951` ordering before terminal `7950`. |
| SFRL-AC-004 | Verified | `docs/soulfactory-runtime-control.md` defines the shared `soulfactory.*` contract. Bahia runtime adapter tests, OpenClaw sidecar tests, and Metiq bridge tests exercise compatible `38384/38386` envelopes, methods, errors, validation, and idempotency. |
| SFRL-AC-005 | Verified | Bahia parses/gates compatible `30317` capabilities; the owned OpenClaw sidecar publishes `runtime=openclaw` capabilities; Metiq publishes/round-trips SoulFactory capability content. |
| SFRL-AC-006 | Verified | Bahia signs/selects runtime requests and requires correlated `38386`; OpenClaw sidecar and Metiq bridge validate trust, addressing, schema/tags/content, idempotency, and replay before side effects. |
| SFRL-AC-007 | Verified | Bahia lifecycle tests passed for canonical `6950/7950` lifecycle progress/results with request/action/soul/agent tags, migration alias behavior, replay idempotency, and no timeout/CLOSED terminal inference in web store tests. |
| SFRL-AC-008 | Verified | The first vertical slice is covered by Bahia + owned OpenClaw sidecar + Metiq deterministic evidence: Bahia proves draft-backed `5950` handling, runtime request construction, final `31951/7950` ordering, and `1950 -> 7950`; the owned OpenClaw sidecar proves `30317`, `38384 -> 38386`, idempotency, provision, and lifecycle execution; Metiq proves compatible `30317`, `38384 -> 38386`, idempotency, provision, and lifecycle execution. |

## Verification evidence from this final pass

### Bahia

Commands run from `/Users/bizarro/Documents/Projects/bahia`:

```text
go test ./internal/soulfactory/... ./cmd/openclaw-soulfactory-sidecar
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
?   	github.com/openagentsinc/bahia/cmd/openclaw-soulfactory-sidecar	[no test files]

go test ./...
ok  	github.com/openagentsinc/bahia/internal/adapters/runtime	2.805s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
ok  	github.com/openagentsinc/bahia/test/integration	(cached)
... all Bahia packages passed or had no test files
```

Commands run from `/Users/bizarro/Documents/Projects/bahia/web`:

```text
npm run test:unit -- --run tests/unit/souls-page.test.js tests/unit/souls-store.test.js tests/unit/nostr-client-parsing.test.js
Test Files  3 passed (3)
Tests       81 passed (81)

npm run build
✓ built in 9.11s
```

Existing unrelated build warnings remained in `src/routes/policies/+page.svelte`, `src/routes/settings/+page.svelte`, and Vite dynamic-import chunking; the build exited successfully.

Observed Bahia evidence includes:

- Relay bus protocol handling in `internal/soulfactory/relay_bus_test.go`.
- Runtime capability discovery and `38384`/`38386` correlation in `internal/soulfactory/runtime_adapter_test.go`.
- Draft-backed provisioning/read-model ordering in `internal/soulfactory/reactor_test.go`, especially `TestDraftBackedRuntimeProvisioningPublishesFinalSoulWithResolvedFields`, which drives a signed `5950` through exact `31952` draft/spec-hash resolution into a runtime request and verifies final `31951` before terminal `7950`.
- Lifecycle `1950 -> 6950/7950` compatibility in `internal/soulfactory/lifecycle_handler_test.go` and `internal/soulfactory/provisioner_full_lifecycle_test.go`.
- Souls UI capability-gated draft/provision/lifecycle tracking in `web/tests/unit/souls-page.test.js`, `web/tests/unit/souls-store.test.js`, and `web/tests/unit/nostr-client-parsing.test.js`.

### Owned OpenClaw sidecar

Product-valid OpenClaw evidence uses:

- `docs/openclaw-soulfactory-sidecar.md`
- `cmd/openclaw-soulfactory-sidecar`
- `internal/soulfactory/openclaw_sidecar.go`
- `internal/soulfactory/openclaw_sidecar_test.go`
- `pstf/features/OPENCLAW_SOULFACTORY_SIDECAR/`

Observed sidecar evidence includes:

- `TestOpenClawSidecarPublishesCompatibleCapability` — signs/publishes compatible kind `30317` `runtime=openclaw` capability content.
- `TestOpenClawSidecarValidatesTrustAddressingAndRequiredParams` — rejects untrusted/misaddressed/malformed `38384` control requests before side effects.
- `TestOpenClawSidecarExecutesProvisionAndPublishesCorrelatedResult` — executes the local command-driver seam from a signed addressed `38384` and publishes signed correlated `38386` for provisioning.
- `TestOpenClawSidecarIdempotentReplayDoesNotRepeatSideEffects` and `TestOpenClawSidecarPersistsIdempotencyAcrossRestart` — exact replay republishes cached result without duplicate side effects.
- `TestOpenClawSidecarExecutesLifecycleSuspend` — verifies one lifecycle method path producing a correlated runtime result.
- Static audit found no `net/http`, `http.Client`, or sleep/polling completion path in the owned sidecar paths. The only REST mention is the explicit prohibition in the command-driver seam comment.

### Metiq / Swarmstr

Commands run from `/Users/bizarro/Documents/Projects/swarmstr`:

```text
go test ./internal/nostr/runtime
ok  	metiq/internal/nostr/runtime	(cached)

go test ./cmd/metiqd -run 'SoulFactory|CapabilityAnnouncement'
ok  	metiq/cmd/metiqd	(cached)
```

Observed Metiq evidence includes:

- `internal/nostr/runtime/capability.go` and `capability_test.go` build/parse SoulFactory `30317` capability content with `soulfactory-runtime-capability/v1`, `soulfactory-runtime-control/v1`, methods, controller pubkeys, and relay hints.
- `cmd/metiqd/soulfactory_bridge.go` validates `soulfactory.*` `38384` envelopes, required tags, controller trust, schema/method/idempotency consistency, target runtime, spec hash, and method params before side effects.
- `cmd/metiqd/soulfactory_bridge.go` executes `soulfactory.provision`, `update`, `suspend`, `resume`, `redeploy`, and `revoke`, and returns documented `38386`-compatible result envelopes.
- `cmd/metiqd/soulfactory_bridge_test.go` covers provision contract envelope, all lifecycle methods, spec hash mismatch, exact replay without side effects, idempotency conflict, missing params, and capability controller advertisement.

## Static protocol audit notes

- No evidence was found that Bahia, the owned OpenClaw sidecar, or Metiq infer terminal lifecycle success/failure from elapsed time, relay closure, or polling.
- OpenClaw sidecar `Run` uses scoped filters, EOSE for backfill transition, and returns subscription closure as an error rather than a terminal lifecycle result.
- Publish paths require relay acceptance (`OK`) through the relay-bus/transport surfaces used by the runtime adapter and sidecar.

## Beads updates

- `bahia-nrjg` is closed: product decision resolved in favor of the owned Bahia sidecar.
- `bahia-8ycd.4` is closed and pushed: owned OpenClaw sidecar implemented and verified.
- `bahia-i0rk.3` can be closed once this PSTF update is committed and pushed.

## Open dependencies / risks

No blockers remain for `bahia-i0rk.3`.

Residual non-blocking hardening: promote runtime-control examples into reusable cross-runtime fixture files to reduce future schema drift.
