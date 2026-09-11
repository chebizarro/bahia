# Verification report — BAHIA_ROUTE_CANARY_NOSTR_PROJECTION (bahia-dblkq)

Branch: `task/bahia-canary-nostr-projection` (base `origin/master` 2520343a).

## Result

All nine acceptance criteria are verified by deterministic tests. The full module passes `go build ./...`, `go vet ./...` and `GOFLAGS=-p=2 go test ./... -count=1`: 76 packages ok, 11 without test files, 0 failures.

## Evidence by criterion

| AC | Evidence | Result |
|----|----------|--------|
| AC1 route outage counted under distinct `route` domain | `TestNostrFleetHealthCountsRouteOutagesAsDistinctDomain`, `TestRouteCanaryOutageIsCountedByFleetHealthGauge` (signed projector output → fleet-health → `/metrics` shows `domain="route",status="unhealthy"} 1`, then `0` after recovery) | PASS |
| AC2 30315 + 30900 by route coordinate, 4903 audit, `internal/kinds`, `hostname` not `route` tag | `TestRouteCanaryProjectorPublishesOutageAsRouteDomainObservables` | PASS |
| AC3 truthful status mapping | `TestRouteCanaryFleetStatusIsTruthful`, `TestRouteCanaryProjectorRecoveryClearsContrast` | PASS |
| AC4 instance status and `service_healthy_route_broken` in tags and content, matches API rule | `TestRouteCanaryProjectorPublishesOutageAsRouteDomainObservables`, `TestRouteCanaryServiceHealthyRouteBrokenMatchesAPI` | PASS |
| AC5 relay-OK-verified, idempotent, partial-failure retry, no replaceable regression | `TestRouteCanaryProjectorRetriesOnlyRejectedPublishes`, `TestRouteCanaryProjectorNeverRegressesReplaceableStateButKeepsAuditHistory`, redelivery assertion in the shape test | PASS |
| AC6 exact route counts: audits are lineage, `d`-less route observable is an error | `TestNostrFleetHealthCountsRouteOutagesAsDistinctDomain`, `TestNostrFleetHealthRejectsRouteObservableWithoutCoordinate` | PASS |
| AC7 self-published echoes reach telemetry; handlers stay gated | `TestSubscriberObserversSeeSelfPublishedEchoWhileHandlersStayGated`, `TestSubscriberHandleEventInvokesHandlersOnlyForNewlyPersistedEvents` | PASS |
| AC9 redelivery never moves counters, gauges or timestamps | `TestNostrFleetHealthRedeliveryDoesNotMoveCounters` (4 redeliveries of each of 4 signed events with the clock advancing), `TestNostrFleetHealthOverLimitRejectionIsCountedOncePerEvent`, `TestBoundedEventIDSetEvictsOldestAndStaysBounded`; a mutation removing the dedupe makes them fail (5 and 3 errors instead of 1) | PASS |
| AC8 malformed payloads rejected; constructor requirements; subscriptions | `TestRouteCanaryProjectorRejectsUnattributablePayloads`, `TestNewRouteCanaryProjectorRequiresBusAndPublisher` | PASS |

## Nostr review checklist (touched scope)

- Kinds come from `internal/kinds` (`NIP38Status`, `CASControlState`, `CASAudit`); no numeric kinds and no new kind.
- Publication goes through `NostrEventPublisher.PublishSignedEvent`, which returns an error unless a relay accepts (`OK accepted=true`). Events are recorded as published only after acceptance, and rejections surface for in-process bus retry.
- Dedupe is bounded per `(kind, route coordinate)`. Replaceable semantics are respected: an older transition never overwrites newer state. Audit facts are immutable and always published.
- The projector reacts to in-process subscriptions: no polling, no timers, no sleeps in tests.
- Evidence and reasons are re-sanitized at the publication boundary. Route coordinates and hostnames never become metric labels.

## Not verified here (open defects)

- D3: the gate never publishes bus events, so gate-declared outages reach the projection only through a later supervisor transition, and never while they persist with an unchanged classification (gate owned by concurrent work).
- D4: fleet-health telemetry is not hydrated from relay history on restart (pre-existing, all domains).
- D5: route state that predates the projector is projected on its next transition.
