# BAHIA_ROUTE_CANARIES — Human-in-the-Loop Decisions

## Resolved during implementation

**Canary expectations are control-plane policy, not signed desired state.**
Expected status, expected body, and TLS expiry window are operational policy. Making them part of `DesiredPublicRoutePlan` would mean tightening an expectation invalidates the hash of every already-deployed route plan and would require a `PublicRouteSchemaVersion` bump. They therefore live in `route_canaries` configuration. If the fleet later requires canary expectations to be signed and reviewed alongside the route, that is a schema-version change and belongs in its own task.

**`tls_expiring` never opens an outage or blocks a deployment.**
A route serving correctly on a certificate that expires in ten days is working. Treating it as an outage would page operators for a non-outage and, worse, would let the gate roll back a functioning deployment over a renewal that has not happened yet. It is surfaced as a warning classification and attached to state.

**A failure streak counts any failing classification, not a per-class streak.**
A route flapping between `dns_unresolved` and `upstream_error` is still down. Per-class streaks would let it evade the failure threshold indefinitely. The reported classification is the latest one.

**Container health annotates route findings but never decides them.**
Feeding `ManagedInstanceHealth` into the classifier would couple route verdicts to supervision timing and make a stale or missing health row change whether an outage is declared. The contrast is joined at the read model and event layer instead.

**Detection does not attempt remediation.**
The supervisor does not reload nginx or restart containers when a route breaks. Recovery is "the route returns". Automatic remediation of a routing layer is a separate authorization question and is not in scope.

## Requires human decision

**Should a route outage automatically trigger remediation?**
The git.sharegap.net incident was resolved by an nginx reload. Bahia can now detect that condition precisely. Whether it should be permitted to act on it — and under what approval tier — is a product and authorization decision, not an implementation one. Deliberately left unimplemented.

**Default enablement.**
`route_canaries.enabled` defaults to false. Turning canaries on fleet-wide, and especially turning `gate_enabled` on, changes deployment outcomes: routes that previously deployed "successfully" while broken will now fail. That rollout decision is an operator call and should be staged.
