# BAHIA_ROUTE_CANARIES — Verification Report

Task: `bahia-managed-route-canaries-and-outage-detection-20260907`
Beads: `bahia-kedzy`
Branch: `task/bahia-route-canaries`

## Scope of verification

This report covers implementation-level verification performed in the repository: unit, adapter, and behavioral tests, plus full-repository build, vet, and test runs. **It does not claim live acceptance.** No probe was run against production hosts, no deployment intent was published, and no fleet service was mutated. Live validation is the authorized operator path and is requested separately.

## Result summary

| AC | Statement | Result |
|---|---|---|
| AC1 | Container-healthy service with a 502 route opens a distinctly classified `upstream_error` outage carrying the healthy-container contrast | PASS |
| AC2 | Post-deploy canary failure blocks the deployment and rolls the route back through backend compensation | PASS |
| AC3 | Outage clears when the route returns | PASS |
| AC4 | Checks are repo-configured, validated at startup | PASS |
| AC5 | Public-edge and LAN/split-DNS perspectives both checked; either failing opens the outage | PASS |
| AC6 | TLS chain verified for the canonical hostname; verification never disabled | PASS |
| AC7 | Bounded, sanitized evidence; assertions match the raw body | PASS |
| AC8 | Route state and append-only lineage operator-visible over the API | PASS |
| AC9 | Near-expiry TLS is a warning that never opens an outage or blocks a deploy | PASS |

## Evidence

Full repository gates, run on the branch:

- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./... -count=1` — all packages pass, no regressions

Targeted suites:

- `internal/domain` — 13 route canary test functions covering classification precedence, multi-perspective reduction, hysteresis, sanitization, key stability, target derivation, and policy validation.
- `internal/adapters/runtime` — 10 route prober tests covering healthy route, stale upstream 502, body mismatch, untrusted TLS, pinned dial address, unresolvable host, refused connection, invalid target, conflicting resolution overrides, and body sanitization.
- `internal/service` — 11 supervisor and gate tests covering outage open/clear, sweep isolation, evaluator faults, gate rollback, gate acceptance, compensation failure reporting, apply-failure propagation, and no-target skip.
- `internal/adapters/routing` — pre-existing suite passes **unmodified** after the compensation refactor, including the seven assertions on the `previous public route restored` failure text.

## Notable verification decisions

**Untrusted TLS is the proof that verification is enabled.** Rather than adding a test-only trust-injection hook to production code, the TLS test points the probe at an `httptest` TLS server whose self-signed certificate the system trust store rejects, and asserts `tls_invalid`. A probe that had `InsecureSkipVerify` set would report the route healthy and fail this test.

**DNS failure is made deterministic without network dependence.** `TestProbeRouteUnresolvableHostIsDNSUnresolved` points the probe's resolver at a closed local port rather than relying on a hostname failing to resolve on the host, which would be environment-dependent.

**No sleep-based waiting.** Hysteresis is exercised by calling the pure evaluator directly with synthetic classifications. Supervisor tests call `EvaluateOnce` synchronously rather than starting `Run` and sleeping. The gate's retry loop is deadline-driven and selects against context cancellation.

## Production-readiness statement

No stubs, mocks, fakes, placeholder adapters, TODO markers, or hardcoded production values were introduced in the touched scope. Every configuration field added in this change is consumed on a production path in the same change. `route_canaries.enabled` fails closed at startup when the section is enabled but unusable, rather than parsing successfully and silently probing nothing.

## Open items

Tracked in Beads as follow-ups, none blocking acceptance of this feature:

- Nostr projection of route outage state as a first-class fleet-health observable.
- Web UI surfacing of `service_healthy_route_broken`.
- Per-route probe interval and expectation overrides.
- Regex body assertions beyond substring matching.

## Live validation requested

Independent validation should confirm, on the live fleet:

1. A managed route with an internal vhost derives both a `public_edge` and an `internal_lan` target under the deployed configuration.
2. Deliberately stopping the origin behind a managed route opens an `upstream_error` outage with `service_healthy_route_broken` true while the container remains healthy.
3. Restoring the origin clears the outage.
4. A route-attach intent whose route does not serve is blocked and rolled back by the gate.

---

# Addendum — live validation 2026-09-07 (candidate `eb877c49`)

Live validation was performed by the authorized operator, not by me. Reported outcome:

| Check | Result |
|---|---|
| Candidate live and healthy | PASS |
| Baseline canary state reaches `route_ok` | PASS |
| Controlled internal nginx fault opens `upstream_error` while Astillero stays container-healthy | PASS |
| Restoring nginx clears the outage back to `route_ok` | PASS |
| Bad-health-path route-only attach blocked by the gate | **FAIL — deployed instead of blocked** |

## Root cause of the gate result

The gate was **not** bypassed. Read-only probes of the live host explain the outcome:

```
/health                            -> HTTP 200, 2252 bytes, sha256 2cb95277…
/__route_canary_expected_failure__ -> HTTP 200, 2252 bytes, sha256 2cb95277…
/definitely-not-a-real-path-9f3c   -> HTTP 200, 2252 bytes, sha256 2cb95277…
```

Astillero serves a single-page-application catch-all: every path returns a byte-identical 200 shell. The "bad" route therefore genuinely served traffic. The gate probed it, observed HTTP 200 inside the default 200–299 range with no body assertion configured, classified `route_ok`, and correctly allowed the deploy.

Two consequences, both real:

1. The test could not have failed the gate as designed, because it did not construct a broken route.
2. More seriously, the configured health path for Astillero is **not a health endpoint** — it is the catch-all shell. Every canary on that route was therefore near-vacuous. The `upstream_error` detection worked only because nginx itself returned a real 502.

## Fixes (defects D3, D4)

- Negative-control probe (`detect_catch_all`), new classification `health_path_not_discriminating`, warning by default and blocking under `require_discriminating_health_path`.
- The no-targets gate path is now logged instead of silently succeeding.
- Added `TestGateBlocksGenuinelyBrokenRouteOnStatusMismatch` (404/500/403) to close the question the live test could not answer: the gate does block and roll back genuinely broken routes.

## Remaining live validation

The gate criterion is still unproven live. A valid re-test needs a route that genuinely fails to serve — not merely an unusual path on a catch-all. Options:

1. Point the route at a port with nothing listening → `connect_failed`.
2. Point the upstream at a service returning a non-2xx → `status_mismatch`.
3. Keep the bad health path but enable `detect_catch_all` + `require_discriminating_health_path` → `health_path_not_discriminating` blocks.

Option 3 most closely matches the original intent. Note it will also block the *current* `/health` route for Astillero, since that path is itself non-discriminating — which is the correct signal, but means the health endpoint should be fixed first.
