# SBOM_WORKFLOW_E2E Verification Report

## Status

Bead `bahia-1qe9.9` verified the production SBOM core and deterministic mocked-relay browser coverage for SBOM epics `bahia-1qe9.1` through `bahia-1qe9.8` against `docs/designs/sbom-real-support.md`, AGENTS.md Nostr rules, PSTF artifacts, and deterministic tests. This report does not establish a live relay/Blossom/backend round trip; true E2E evidence must be tracked separately.

## Verification summary by epic

- `bahia-1qe9.1` — Verified. Protocol/docs canonicalize SBOM reference events as kind `30078`, availability lists as kind `30004`, and legacy `30079` as read-only compatibility.
- `bahia-1qe9.2` — Verified. Subject-neutral domain, migration, repository projection, package index, and artifact compatibility projection are implemented. Earlier `bahia-bl58` migration-manifest concern is resolved by current `go test ./internal/...` passing.
- `bahia-1qe9.3` — Verified. Parser and attestation helpers produce subject-neutral manifest/package results while preserving artifact compatibility; attestation subject/payload digest verification is covered.
- `bahia-1qe9.4` — Verified. Syft is a real in-process generator, cdxgen is an optional executable adapter with deterministic tests, and `bahia-qfod` wires runtime operator configuration for cdxgen enablement.
- `bahia-1qe9.5` — Verified. Event builders produce required `30078` reference and `30004` availability-list tags; publishing requires relay OK results and rejects auth/closed/OK false outcomes. Exact payload hash verification is performed in the orchestration/storage path before reference publication.
- `bahia-1qe9.6` — Verified. The orchestrator enforces Blossom storage, verifies payload hash and attestation digests, publishes status/audit/reference/list observables with OK verification, serializes per-subject work, and avoids `30079`. Package/repository subject locator ambiguity is resolved by `bahia-wqj5`; ContextVM ack-vs-completion semantics are resolved by `bahia-ilio`.
- `bahia-1qe9.7` — Verified after fix. REST `POST /artifacts/{id}/sbom` delegates to the import service and no longer bypasses Blossom/Nostr. Verification fixed oversized payload handling so bodies over 10 MiB return `413` instead of being truncated and imported.
- `bahia-1qe9.8` — Verified for PSTF acceptance criteria, test matrix, defects, user docs, and deterministic injected-relay browser coverage. `bahia-wf2k` is closed with that generate/import UI and injected-relay coverage; it does not constitute live relay/Blossom/backend E2E evidence.

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

Existing deterministic mocked-relay browser evidence from prior SBOM PSTF work remains applicable:

```bash
cd web && pnpm test:e2e --reporter=line tests/e2e/environments-crud-smoke.spec.js tests/e2e/sbom-workflow.spec.js
```

## Web artifact generate/regenerate mocked-relay browser slice — 2026-06-13

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

## bahia-qfod cdxgen runtime config — 2026-06-30

- `internal/config/config.go` now exposes `sbom.cdxgen.enabled` and `sbom.cdxgen.binary_path`, defaults cdxgen to disabled, and maps `BAHIA_SBOM_CDXGEN_ENABLED` / `BAHIA_SBOM_CDXGEN_BINARY_PATH` into the nested config.
- `internal/app/app.go` now constructs the production SBOM generator registry with `NewCdxgenGenerator` when cdxgen is enabled; disabled config preserves Syft fallback.
- `internal/adapters/sbom/generator_test.go`, `internal/app/sbom_generator_registry_test.go`, and `internal/config/config_test.go` cover disabled, missing-binary, explicit cdxgen request, and auto fallback/selection behavior deterministically.
- `docs/user-guide/getting-started.md` and `docs/user-guide/features/artifacts.md` document the new YAML and environment variable fields.

Targeted gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/config ./internal/adapters/sbom ./internal/app
```

Result: PASS on 2026-06-30.

Broad gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## bahia-wqj5 package/repository subject locators — 2026-06-30

- `internal/domain/sbom.go` now defines `SBOMSubjectLocator`, `SBOMPackageArtifactLocator`, and `SBOMRepositoryLocator`.
- `internal/service/sbom_orchestrator.go` now resolves package subjects from package artifact coordinates plus SHA-256, and repository subjects from either validated git commit object IDs or immutable `sha256:<64-hex>` content digests.
- `internal/controlplane/sbom_handlers.go` forwards `subjectLocator` for `sbom/import`; `sbom/generate` unmarshals the same field through the service request.
- `docs/designs/sbom-real-support.md`, `docs/user-guide/features/packages.md`, and `docs/user-guide/nostr-integration.md` document the canonical request fields.
- `pstf/features/bahia-wqj5/hitl_decisions.md` records the immutable-locator HITL decision.

Targeted gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/domain ./internal/service ./internal/controlplane
```

Result: PASS on 2026-06-30.

Broad gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## bahia-ilio asynchronous ContextVM SBOM acknowledgments — 2026-06-30

- `internal/service/sbom_async_runner.go` adds a managed, channel-driven SBOM async runner. It computes deterministic `sbom:run:<sanitized-idempotencyKey>` status d-tags, returns `service.SBOMAcceptedAck`, and performs Generate/Import work off the ContextVM request path without polling, sleeps, or timeout-based completion.
- `internal/controlplane/sbom_handlers.go` now enqueues `sbom/generate` and `sbom/import` requests and returns accepted idempotency/status coordinates instead of synchronous `SBOMRunResult` completion payloads. The import path still decodes inline payloads and forwards `subjectLocator`.
- `internal/app/app.go` registers the SBOM async runner with `BackgroundManager` and wires ContextVM handlers to that runner.
- `internal/service/sbom_async_runner_test.go` injects OK accepted, OK rejected, CLOSED before EOSE, and AUTH outcomes deterministically through existing orchestrator fakes and waits on runner result channels rather than sleeps.
- `docs/control-planes.md`, `docs/designs/sbom-real-support.md`, and `docs/user-guide/nostr-integration.md` document the HITL Option A acknowledgment contract.
- `pstf/features/bahia-ilio/hitl_decisions.md` records the explicit asynchronous-ack decision.

Targeted gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/service ./internal/controlplane ./internal/app
```

Result: PASS on 2026-06-30.

Broad gate:

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## bahia-wf2k artifact SBOM import mocked-relay browser workflow — 2026-06-30

- `web/src/lib/stores/public-controlplane.svelte.js` now exposes `importArtifactSBOM(...)` for signer-backed ContextVM `sbom/import` intents. It uses `publishCommandOnly`, tags the artifact subject/format/generator, sends the backend `sbomImportParams` payload shape (`idempotencyKey`, `subject`, `format`, inline `payloadBase64` or `location`, `storage`, `generator`), and rejects inline payloads over the 512 KiB ContextVM limit before publishing.
- `web/src/routes/artifacts/[id]/+page.svelte` now exposes an **Import SBOM** control on the SBOM tab with JSON file picker, SPDX/CycloneDX format selection/detection, client-side 512 KiB validation, base64 inline import, and operation-specific errors/status. Import reuses the same artifact-scoped `30078`/`30004` subscription and refresh path as generate; the ContextVM ACK remains pending-only and terminal UI success comes from canonical observable events.
- `web/tests/e2e/helpers.js` now handles deterministic `sbom/import` browser intents and injects OK-published `30315`, `4903`, `30078`, `30004`, Blossom reference data, and artifact compatibility projection events without sleeps in the spec. The mock WebSocket now performs a realistic CONNECTING→OPEN transition and supports `addEventListener` so initial relay bootstrap is deterministic.
- `web/tests/e2e/sbom-workflow.spec.js :: artifact SBOM tab publishes signer-backed ContextVM import request and completes from canonical SBOM events` drives the file picker and asserts: Blossom reference, 30078 reference, 30004 availability-list entry, 30315 status, 4903 audit, compatibility projection, and async ACK-only semantics (`reference_event_ids`/`availability_event_id` absent from ACK).
- PSTF defect `D3` is cleared to `fixed_pending_user_verification`; test matrix now maps the import unit/E2E coverage to AC2, AC3, AC4, AC5, AC7, and AC8.

Targeted gates:

```bash
cd web && pnpm exec vitest run --config vitest.config.js tests/unit/public-controlplane.test.js
cd web && pnpm lint
cd web && pnpm exec playwright test --reporter=line tests/e2e/sbom-workflow.spec.js -g "ContextVM import request" --workers=1
cd web && pnpm exec playwright test --reporter=line tests/e2e/sbom-workflow.spec.js --workers=1
cd web && CI=true pnpm install --frozen-lockfile
```

Results: PASS on 2026-06-30. Playwright required unsandboxed browser launch on macOS after the sandboxed run failed before assertions with `MachPortRendezvousServer ... Permission denied`. The first frozen install attempt without `CI=true` aborted because pnpm required non-interactive confirmation to recreate `node_modules`; the `CI=true` frozen install passed.

Full unit-suite status:

```bash
cd web && pnpm test:unit
```

Result: FAIL on 2026-06-30 due to unrelated existing unit-suite failures outside the SBOM import slice (for example missing `../../src/lib/nostr/pool-query.js`, route matrix entries for `web/src/routes/security/*`, and stale mocks lacking `nostr`/`queryUntilEose` exports). The focused public-controlplane unit file passed with the direct Vitest command above.

## bahia-rxae relay-backed web fixture restoration — 2026-07-03

- Restored the SBOM browser E2E fixture for current relay-native behavior: discovery now advertises `features.encrypted_nostr_requests` and `contextvm_relays`, public SBOM generate/import commands are asserted as canonical unwrapped ContextVM kind `25910`, and completion remains driven by injected canonical `30315`, `4903`, `30078`, and `30004` relay events.
- Updated `web/tests/e2e/sbom-workflow.spec.js` so artifact registry coverage matches current UI behavior: the registry list exposes row navigation plus SBOM status, while generate/import actions are exercised from the artifact detail SBOM tab.
- Replaced unresolved browser-side promise waits for SBOM generated/imported fixture events with Playwright `exposeFunction` callbacks, keeping the spec event-driven without sleep waits.

Targeted gate:

```bash
cd web && npm run test:e2e -- sbom-workflow.spec.js service-secrets-smoke.spec.js
```

Result: PASS on 2026-07-03 — 12 passed. Playwright required unsandboxed browser launch on macOS after sandboxed Chromium launch failed before assertions with `MachPortRendezvousServer ... Permission denied`.

## Remaining tracked work

- `bahia-ndmr`: Relay-backed merge of existing canonical 30004 availability-list entries across Bahia instances remains tracked.
- A true live relay/Blossom/backend end-to-end run is outside the evidence in this report and must be tracked separately if required; do not treat closed `bahia-wf2k` as that evidence.


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

## Web artifact SBOM completion feedback — 2026-06-14

- Fixed Bead `bahia-1puw`: artifact **Generate SBOM** now switches to the SBOM tab, opens a scoped Nostr subscription for artifact `30078` SBOM reference events and `30004` availability-list events before publishing `sbom/generate`, and renders generated format, generator, Blossom URI, and payload hash when canonical events arrive.
- The ContextVM response is now presented only as request/pending state. Durable completion is driven by canonical SBOM events, not by the encrypted ContextVM acknowledgment.
- `web/tests/e2e/helpers.js` now emits mock service-published SBOM events to active relay subscriptions so browser E2E covers EVENT-driven completion rather than localStorage-only inspection.

Verification:

```bash
cd web && npm run test:e2e -- --reporter=line tests/e2e/sbom-workflow.spec.js
cd web && npm run lint
```

Result: PASS on 2026-06-14.

## Signed DSSE attestations — 2026-07-17

- `domain.SBOMAttestation` now carries a DSSE envelope with the exact in-toto statement payload and BIP-340 signature metadata.
- The SBOM orchestrator signs DSSE pre-authentication encoding with Bahia's configured Nostr service key before building or publishing a `30078` reference; no new or generated production key material is introduced.
- `BuildSBOMReferenceEvent`, `ParseAttestation`, `ParseAttestationFromEvent`, and `StorageResolver.ResolveAndVerify` reject unsigned or tampered statements. Relay reads additionally require the DSSE key ID/signature to match the cryptographically verified Nostr event publisher.
- Snapshot republishing signs reconstructed statements with the projector's existing configured service key.
- Regression tests cover unsigned rejection, valid signed verification, statement tampering, untrusted keys, event-publisher binding, and orchestrator publication of a verifiable DSSE envelope.

Targeted verification:

```bash
go test ./internal/domain ./internal/adapters/sbom ./internal/adapters/nostr ./internal/service ./internal/app
```

Result: PASS on 2026-07-17.

Full quality gate:

```bash
go build ./...
go test ./...
```

Result: PASS on 2026-07-17.
