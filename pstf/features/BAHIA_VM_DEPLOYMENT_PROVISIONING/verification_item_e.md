# Item E verification — public surfaces and evidence

Scope: `bahia-8f30n`, epic `bahia-yrt7g`; branch
`feat/vm-deployment-provisioning`. This supplement does not change Item A's
specification, acceptance criteria, repository contracts or verification report.

## Recorded orchestration decision

Use `events.InProcessPublisher` for post-commit live signals and
`VirtualizationRepository.ListChanges` for startup/gap recovery. No PostgreSQL
LISTEN/NOTIFY producer or migration is required. The decision and integration
contract are recorded in comments on `bahia-8f30n`.

## Acceptance mapping

| Criterion | Evidence |
|---|---|
| E-1: all seven resource families use public allowlists; no bootstrap, raw diagnostics, storage locations or arbitrary labels leak | `TestVirtualizationPublicAllowlist`, `TestVirtualizationRejectsUnknownPublicEnums` |
| E-2: REST requires authenticated key and tenant permission even with auth disabled; query-only routes, bounded pagination | `TestVirtualizationHTTPAuthorizationAndPublicData`, `TestVirtualizationRoutesRegisteredReadOnlyAndFailClosed`, `TestVirtualizationQueryTenantFenceAndPagination` |
| E-3: ContextVM methods registered, principal derived from validated transport, tenant/role checks, missing services/projection unavailable, sanitized errors | `TestVirtualizationContextVMRegistrationPrincipalAndUnavailable`, `TestVirtualizationMutationErrorsNeverExposeProviderEvidence`, `TestVirtualizationSignedTransportMetadataAndReplay` |
| E-4: replay preserves every audit, coalesces snapshots, detects corrupt identity, covers missing/duplicate signals and durable cursor restart | `TestVirtualizationJournalRecoveryCoalescesStatePreservesAudit`, `TestVirtualizationRejectsCorruptJournal`, `TestVirtualizationLiveBusAndShutdown` |
| E-5: signing/storage failure cannot advance cursor; pending relay rejection remains durable; cursor retry reuses signed output; same-coordinate timestamps rate-limited | `TestVirtualizationDurableAdmissionAndCursorFailureRetry`, `TestVirtualizationJournalRecoveryCoalescesStatePreservesAudit` |
| E-6: bounded OTel and endpoint instruments, aggregate retraction, no raw span/error/evidence strings, concurrent recording | `TestVirtualizationMetricsBoundedAndMirrored`, `TestVirtualizationMetricsConcurrentAndSanitizedSpan`, `TestVirtualizationCollectorAggregatesAndRetracts` |
| E-7: injected admission -> committed journal -> live bus -> signed public outbox; startup and shutdown; nil dependencies fail closed | `TestVirtualizationCompositionAdmissionProjectionAndShutdown`, `TestVirtualizationCompositionMissingDependenciesFailClosed` |

Tests use injected repositories/provider-facing admission boundaries and the real
DTO, readmodel, in-process event bus, signer, cursor/outbox repository, OTel SDK
and HTTP/ContextVM handlers. Completion is synchronized by channels and durable
checkpoints, not sleeps. No live VM or relay acceptance is claimed.

## Commands and results

The package set is:

```text
./internal/api/dto ./internal/api/handlers ./internal/controlplane
./internal/readmodel ./internal/adapters/telemetry ./internal/api/router
./internal/app ./internal/events ./internal/kinds
```

- `go test <packages> -run Virtualization`: passed.
- `go test <packages>`: passed, including existing package tests.
- `go build <packages>` and `go build ./...`: passed.
- `go vet <packages>`: passed.
- `go test -race <packages>`: passed as part of the full-project plain
  `go test -race ./...` re-verification for `bahia-4fz4z` on 2026-09-22.
  Upstream `fiatjaf.com/nostr` `v0.0.0-20260916040958-27e395a0f6e7` fixes the
  JSON string pointer arithmetic that previously aborted signing tests.
  No checkptr exemption or test skip is needed. See the dependency-upgrade
  evidence in `verification_report.md`; PostgreSQL-tagged and live acceptance
  were not rerun for this change.

An early build hit concurrent Item B's unfinished `timeNow` references; a later
full build passed after sibling progress. No sibling files were edited to obtain
a pass.

## Registration and remaining integration

- Actual constructor/composition: `internal/app/app.go`, `New`.
- Router: `internal/api/router/router.go`, `NewWithDeps`, invoking
  `RegisterVirtualizationRoutes` from `internal/api/router/virtualization.go`.
- ContextVM registry: `internal/controlplane/encrypted_transport.go`,
  `RegisterContextVMHandler`; `app.go` invokes the E handler registration.
- E composition: `internal/app/virtualization.go`, `NewVirtualization`.

The orchestrator supplies `VirtualizationDependencies.PersistentVM` and
`.ExecutionPlane` adapters implementing the E-owned admission interfaces in
`internal/controlplane/virtualization.go`. Without them, mutations are unavailable.
C/D publish typed `events.VirtualizationChange` wakeups after successful durable
mutations and call bounded telemetry helpers for provider operations, approval
rejections, console activity, probe results and effective capability changes.
E's repository aggregate collector is already composed for startup/live updates.
These integration tasks remain within epic `bahia-yrt7g`; Item E does not claim
C/D wiring, multi-instance projector leadership, live provider/relay acceptance,
image builds, desktop pilots or soak. Run one active projector per signer.

No Item A/B/C/D files, migrations, numeric kind allocations or orchestrator-owned
plans were edited by Item E. No push or Oracle review was performed.
