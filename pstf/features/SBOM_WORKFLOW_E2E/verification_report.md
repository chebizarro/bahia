# SBOM_WORKFLOW_E2E Verification Report

## Status

Bead `bahia-1qe9.9` verified completed SBOM epics `bahia-1qe9.1` through `bahia-1qe9.8` against `docs/designs/sbom-real-support.md`, AGENTS.md Nostr rules, PSTF artifacts, deterministic tests, and production-readiness constraints.

## Verification summary by epic

- `bahia-1qe9.1` — Verified. Protocol/docs canonicalize SBOM reference events as kind `30078`, availability lists as kind `30004`, and legacy `30079` as read-only compatibility.
- `bahia-1qe9.2` — Verified. Subject-neutral domain, migration, repository projection, package index, and artifact compatibility projection are implemented. Earlier `bahia-bl58` migration-manifest concern is resolved by current `go test ./internal/...` passing.
- `bahia-1qe9.3` — Verified. Parser and attestation helpers produce subject-neutral manifest/package results while preserving artifact compatibility; attestation subject/payload digest verification is covered.
- `bahia-1qe9.4` — Verified with tracked follow-up. Syft is a real in-process generator and cdxgen is an optional executable adapter with deterministic tests. Runtime operator wiring for cdxgen enablement remains tracked as `bahia-qfod`.
- `bahia-1qe9.5` — Verified. Event builders produce required `30078` reference and `30004` availability-list tags; publishing requires relay OK results and rejects auth/closed/OK false outcomes. Exact payload hash verification is performed in the orchestration/storage path before reference publication.
- `bahia-1qe9.6` — Verified with tracked follow-ups. The orchestrator enforces Blossom storage, verifies payload hash and attestation digests, publishes status/audit/reference/list observables with OK verification, serializes per-subject work, and avoids `30079`. Package/repository subject locator ambiguity remains tracked as `bahia-wqj5`; ContextVM ack-vs-completion semantics are tracked as `bahia-ilio`.
- `bahia-1qe9.7` — Verified after fix. REST `POST /artifacts/{id}/sbom` delegates to the import service and no longer bypasses Blossom/Nostr. Verification fixed oversized payload handling so bodies over 10 MiB return `413` instead of being truncated and imported.
- `bahia-1qe9.8` — Verified. PSTF acceptance criteria, test matrix, defects, and user docs cover the real SBOM flow. Browser E2E for signer-first generate/import UI/control flows remains tracked as `bahia-wf2k`.

## Verification fixes made in `bahia-1qe9.9`

- `internal/service/sbom_orchestrator.go`: moved local manifest projection until after accepted `30078` reference and accepted `30004` availability-list publication, then persisted `availability_event_id` and `availability_d_tag` in the projection. This restores the design order where Nostr observables are canonical and the database is a projection.
- `internal/service/sbom_orchestrator_test.go`: added deterministic assertion that projection happens after availability-list publication and that the projected manifest contains the availability d-tag.
- `internal/api/handlers/sbom.go`: changed REST compatibility ingest to reject payloads larger than 10 MiB instead of silently truncating with `io.LimitReader`.
- `internal/api/handlers/sbom_test.go`: added oversized-payload coverage proving no import service call occurs after a `413` rejection.

## Quality gates run

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/sbom ./internal/service ./internal/api/handlers ./internal/repository ./internal/adapters/nostr ./internal/mcp
```

Result: PASS on 2026-06-13.

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/controlplane
```

Result: PASS on 2026-06-13.

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-13.

Existing targeted UI evidence from prior SBOM PSTF work remains applicable:

```bash
cd web && pnpm test:e2e --reporter=line tests/e2e/environments-crud-smoke.spec.js tests/e2e/sbom-workflow.spec.js
```

## Web artifact generate/regenerate slice — 2026-06-13

- `web/src/lib/stores/public-controlplane.svelte.js` now builds encrypted ContextVM `sbom/generate` requests for artifact subjects with immutable digest, explicit OCI image source locator, SPDX/CycloneDX formats, Syft generator, Blossom storage, scoped tags, and deterministic idempotency keys. It rejects display-name-only artifacts instead of treating names such as `nostrodomo` as Docker Hub image repositories.
- `web/src/routes/artifacts/[id]/+page.svelte` now exposes **Generate SBOM** and **Regenerate SBOM** from the artifact detail header and SBOM tab, publishes through the Nostr ContextVM helper, does not call REST generation endpoints, and tells users that durable completion is reflected by SBOM reference and availability-list events.
- `web/src/routes/artifacts/+page.svelte` now exposes a per-row **Generate SBOM** or **Regenerate SBOM** link from the registry list, routing directly to the artifact SBOM tab with `?tab=sbom`.
- `web/tests/e2e/helpers.js` can now deterministically observe gift-wrapped SBOM generate requests and inject `30315`, `4903`, `30078`, and `30004` mock relay outcomes for browser verification without sleeps.

Targeted gates for this slice:

```bash
cd web && npm run test:unit -- tests/unit/public-controlplane.test.js
cd web && npm run test:e2e -- --reporter=line tests/e2e/sbom-workflow.spec.js
```

Results: PASS on 2026-06-13. A focused Oracle review flagged two fixes before final gates: the UI now keeps the explicit ContextVM request event ID instead of displaying the result event ID, and the production SBOM path remains on the default encrypted result kind rather than accepting raw `25910` results for test convenience.

## Remaining tracked work

- `bahia-wqj5`: Define canonical package and repository ContextVM subject locators before enabling automatic digest lookup for ambiguous package/repository subjects.
- `bahia-wf2k`: Add live browser E2E coverage for signer-first `sbom/generate`/`sbom/import` UI/control flows with injected EVENT/EOSE/OK outcomes and terminal truth from `30078` plus `30004`.
- `bahia-qfod`: Wire optional cdxgen runtime configuration into app construction so operators can enable the executable adapter.
- `bahia-ilio`: Record and implement/document the intended ContextVM acknowledgment versus synchronous completion semantics for SBOM generate/import.

## Nostr/PSTF notes

- No sleep-based or polling completion logic was found or added in the verified SBOM production scope.
- Canonical completion remains observable through accepted Nostr events (`30078` and `30004`), with `30315` status and `4903` audit evidence.
- Legacy `30079` remains read-only compatibility data; new canonical SBOM availability uses `30004`.
- Touched SBOM production scope contains no fake, stubbed, hardcoded, or placeholder behavior; remaining gaps are tracked in Beads.

## Oracle review follow-up — 2026-06-13

A focused Oracle review of the critical SBOM orchestration/event/REST/ContextVM files flagged pre-commit issues. The following fixes were applied before final quality gates:

- Added idempotency-key locking and re-checking in `SBOMOrchestrator` to prevent concurrent duplicate side effects for the same idempotency key.
- Verified accepted publishes are signed by the configured SBOM publisher pubkey before using that pubkey in 30004 `a` coordinates.
- Corrected availability-list entries built from existing projections to use the SBOM publisher pubkey rather than the generator pubkey.
- Made post-canonical `projecting`, audit, and completed-status publish failures non-blocking for local projection after accepted 30078/30004 publication.
- Added strict 64-character hex SHA-256 validation for 30078 `x` payload hashes and 30004 availability entries.
- Split SBOM REST handler construction into read-only and write/import constructors so the write route is only mounted with a configured importer.
- Added a 512 KiB ContextVM inline `payloadBase64` limit for `sbom/import`; larger imports must use location-based or REST compatibility import paths.
- Created Bead `bahia-ndmr` and PSTF defect `D9` for relay-backed merge of existing canonical 30004 availability-list entries across Bahia instances.

Final gate after these fixes:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS.
