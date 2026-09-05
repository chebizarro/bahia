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
- `cascadia-go` v1.2.1 does not yet generate a release-provenance binding for
  the producer-specific terminal RELEASE 5402 tags/content. Bahia uses the
  centralized mirrored adapter and generated generic kind/method constants.

## Verification evidence

- `go test ./internal/adapters/hiveci ./internal/pipeline ./internal/controlplane ./internal/service ./internal/workflow ./internal/app` passed on 2026-08-22.
- `go test ./... -count=1` passed on 2026-08-22.
- `go build ./...` passed on 2026-08-22.
- `golangci-lint run --new-from-rev HEAD ./...` passed with 0 issues.
- Full `golangci-lint run ./...` remains non-zero on the repository's unchanged 155-issue baseline (50 errcheck, 5 ineffassign, 50 staticcheck, 50 unused); no phase-3 issue was reported.