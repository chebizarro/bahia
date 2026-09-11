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
| AC10 gate transitions projected exactly like supervisor transitions (added with the D3 fix) | `TestGateRecoveryClearsSupervisorOutageInNostrProjection`, `TestGateOpenedOutageIsPublishedProjectedAndRolledBack`, `TestGateAndSupervisorPublishTheCanonicalTransitionPayload`, `TestRouteCanaryTransitionEventMapsEveryTransition` | PASS |

## Nostr review checklist (touched scope)

- Kinds come from `internal/kinds` (`NIP38Status`, `CASControlState`, `CASAudit`); no numeric kinds and no new kind.
- Publication goes through `NostrEventPublisher.PublishSignedEvent`, which returns an error unless a relay accepts (`OK accepted=true`). Events are recorded as published only after acceptance, and rejections surface for in-process bus retry.
- Dedupe is bounded per `(kind, route coordinate)`. Replaceable semantics are respected: an older transition never overwrites newer state. Audit facts are immutable and always published.
- The projector reacts to in-process subscriptions: no polling, no timers, no sleeps in tests.
- Evidence and reasons are re-sanitized at the publication boundary. Route coordinates and hostnames never become metric labels.

## Not verified here (open defects)

- D4: fleet-health telemetry is not hydrated from relay history on restart (pre-existing, all domains).
- D5: route state that predates the projector is projected on its next transition.

---

# Addendum — gate transitions reach the projection (D3 resolved)

Branch: `task/bahia-canary-integration-fix-go` (integration `22b84636`).

## Why it mattered more than D3 recorded

Independent review of the combined tree showed that D3 caused a false outage that never cleared, not just a delay:

1. The supervisor opens an outage, and the projector publishes 30315/30900 as `unhealthy`/`open`.
2. A fix is deployed and the post-deploy gate passes. It records `Recovered` (thresholds 1/1) but publishes nothing.
3. Later sweeps see a closed `route_ok` and report no transition.
4. Nostr and `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"}` stay on the outage indefinitely, and no `route.canary_recovered` is announced. Gate-opened outages never reached Nostr at all.

## Fix

- `RouteCanaryGate` takes an `events.Publisher`, wired in `internal/app/app.go` to the same bus as the supervisor.
- Both the gate and the supervisor publish through `publishRouteCanaryTransition`. That is the one place a route transition becomes an event: `routeCanaryTransitionEvent` maps transition to event type and severity and builds the `RouteCanaryChanged` payload.
- The gate publishes only transitions whose state and lineage were persisted. It publishes on `context.WithoutCancel`, so a cancelled deployment still announces a durable transition. Rollback stays on its own non-cancelled context.

## Evidence

`internal/service/route_canary_gate_publish_test.go` drives the real supervisor, gate, projector and fleet-health telemetry through a synchronous recording bus with fixed clocks, with no sleeps and no goroutine waits:

- **Recovery path.** Supervisor opens (gauge `unhealthy 1`), then the gate passes (`route.canary_recovered`, severity `info`, 30315/30900 `healthy`/`closed`, gauge `unhealthy 0` and `healthy 1`). A later sweep publishes nothing and the gauge stays healthy.
- **Opened path.** A gate-opened outage is published (`critical`), projected `unhealthy`/`open` with `service_healthy_route_broken=true`, and rolled back. A later supervisor sweep of the same failure adds nothing, and the gauge stays `unhealthy 1`.
- **Canonical payload.** The gate (opened, recovered, reclassified) and the supervisor (opened) each publish exactly `routeCanaryTransitionEvent(persisted state, persisted lineage)`, compared with deep equality.
- **Edge cases.** Unpersisted transitions are not published. A cancelled deployment still announces on a live context and rolls back on a live context.

Two mutations were checked. Removing the gate's publish call fails four of these tests. Publishing on the raw deployment context fails the cancellation test.
