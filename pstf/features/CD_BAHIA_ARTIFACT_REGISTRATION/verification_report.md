# CD_BAHIA_ARTIFACT_REGISTRATION Verification Report

## Scope

Implemented producer-accurate accepted-release ingestion, digest-only Bahia artifact
registration, and a separate staged canary authorization path for
`cd-bahia-artifact-registration-20260821`.

## Acceptance mapping

- **AC1 / AC2:** Hive-CI adapter, bridge, and registry tests cover signatures,
  admitted workers, repository/workflow/trigger/review lineage, descriptor and
  digest verification, exact replay, and conflict.
- **AC3:** bridge and registry tests prove CI registration creates neither an
  intent nor desired-state change; pending intents remain proposals.
- **AC4:** `TestAcceptedReleaseContextVMPromotionCreatesDigestOnlyCanary`
  registers an accepted release, passes the signed request through the actual
  ContextVM `service/deploy` handler, and executes the resulting approved intent
  through the coordinator's Loom canary seam using `repository@sha256:digest`.
- **AC5:** authorization, evidence, rollback, contract, replay, outbox, and
  no-side-effect rejection tests cover the promotion boundary.

## Protocol limits

- Promotion supports the staged `canary` strategy only. Widening or production
  promotion remains a separate future authorization.
- A previous desired artifact is mandatory so rollback compatibility can be
  checked before intent creation.
- Health/readiness contracts must include a non-empty `type` and positive
  `timeout_seconds`; Bahia forwards their canonical JSON to the Loom canary.
- 2026-09-19: release evidence moved off Hive-CI kind 5402 (hive-ci-protocol
  defines a single 5402 semantic) onto the cascadia-nips `release_attestation`
  contract: kind 4903 `domain=release`/`type=attestation`/`schema=bahia.audit.release.v1`
  with `run=<5401 id>`, canonical envelope, and the full
  `hiveci.release-provenance.v1` document under `meta.hiveci_release`.
  `TestReleaseIngestorAcceptsOnlyKind4903ReleaseAttestations` proves a 5402
  (ordinary or `result=RELEASE`) is never accepted. The `cascadia-go` generated
  bindings do not yet carry the envelope type; Bahia defines it in `domain`.

## Verification evidence

- `go test ./internal/adapters/hiveci ./internal/pipeline ./internal/controlplane ./internal/service ./internal/workflow ./internal/app` passed on 2026-08-22.
- `go test ./... -count=1` passed on 2026-08-22.
- `go build ./...` passed on 2026-08-22.
- `golangci-lint run --new-from-rev HEAD ./...` passed with 0 issues.
- Full `golangci-lint run ./...` remains non-zero on the repository's unchanged 155-issue baseline (50 errcheck, 5 ineffassign, 50 staticcheck, 50 unused); no phase-3 issue was reported.