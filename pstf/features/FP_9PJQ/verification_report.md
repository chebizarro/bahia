# FP-9pjq Hygiene Freshness Verification

Source: `fp-9pjq`; design review: `fleet-planning/docs/designs/hygiene-observation-transport.md` section 8.3.4.

## Implementation

Each worker pass now captures an injected clock value before publishing its scan and pressure requests. The observation cutoff is derived from that immutable pass-start timestamp rather than `time.Since` after the round-trip. This preserves the one-interval policy without blocking on worker liveness or widening the accepted stale-data window.

## Verification

Run on 2026-09-02:

- `go test ./internal/reconcile -run 'TestHygieneReconciler(FreshnessUsesPassStartBoundary|IssuesScanAndTier1Actions|PressureBreachTriggersGC|RespectsAutoFlagsAndDisabledPolicy)$' -count=1` — PASS.
- `go test ./internal/reconcile -count=1` — PASS.

The regression uses a fixed clock and advances it by 15 seconds for each of scan and pressure publication. The exact one-interval boundary remains accepted after that simulated latency; one nanosecond older remains rejected.
