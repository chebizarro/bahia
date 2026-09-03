# FP-20aa Hygiene Observation Transport Verification

Source: `fp-20aa`; governing design: `fleet-planning/docs/designs/hygiene-observation-transport.md`.

## Implementation

- Bahia now consumes maintenance observations from canonical JSON-RPC responses on ContextVM kind `25910`, not from kind `30315`.
- The existing encrypted transport dispatches responses only after NIP-59 unwrap/provenance, inner validation, routing, replay, and deduplication gates. Response handling remains before request authorization and never emits a response to a response.
- The maintenance publisher registers the exact finalized Bahia rumor ID, Bahia pubkey, worker pubkey, method, and JSON-RPC id before calling the relay publisher. Definitive zero-accept publication cancels the correlation; partial/ambiguous delivery retains it until response or bounded expiry.
- The source accepts only NIP-59 responses whose authenticated inner author is the addressed worker and whose sole `p`, sole `e`, and JSON-RPC id match the registered request.
- `cascadia-go v1.4.0` generated result bindings validate the payload. Bahia additionally enforces scan total/truncation consistency and pressure free-versus-total invariants.
- Scan and pressure freshness are independent but retain the `fp-9pjq` pre-scan, one-interval cutoff.

## Truncation decision

A valid `truncated=true` scan overwrites the previous scan component, records `total_candidates`, and caches zero candidates. The reconciler explicitly suppresses candidate convergence for that scan. It does not quarantine from a partial prefix. A separately authenticated, canonical, fresh pressure result remains eligible for policy-authorized GC.

## Verification

Verification on 2026-09-02:

- `go test ./internal/controlplane ./internal/reconcile ./internal/app -count=1` — PASS.
- `go test ./... -count=1` — PASS.
- `go vet ./...` — PASS.
- `make build` — PASS for every build target.
- `go test -race ./internal/reconcile -run 'TestHygieneReconciler|TestHygieneObservationSource(DoesNotRegress|Bounds)' -count=1` — PASS.
- `go test -race ./... -count=1` — blocked by the tracked baseline `fp-z70q`: unchanged `fiatjaf.com/nostr/event.go:245` fails Go `checkptr` in packages that sign events. No checkptr disablement or dependency replacement was added.
- `make lint` — reports the repository baseline of 155 existing findings. The only findings in a modified pre-existing source file are the already-present `matchesRoutingTags` and `publishError` unused methods, introduced months before this change; no new FP-20aa code is reported.
- `git diff --check` — PASS.
