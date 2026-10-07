// Package archtest holds Bahia's architecture ratchet tests (bahia-irsry.8,
// audit "Phase 0" and "Preventive Measures").
//
// Each gate walks the module's Go sources with go/packages type information
// and compares what it finds against a checked-in baseline under testdata/.
// A gate fails only when a file gains violations beyond its baseline, so the
// existing debt is recorded and can only shrink. The gates are:
//
//   - legacy kind constants outside internal/nostrmigration (C-44);
//   - direct Subscribe/Fetch calls on fiatjaf.com/nostr Relay or Pool objects
//     outside the relay pool and SoulFactory bus (C-8, C-35);
//   - time.NewTicker/time.Tick in internal/service and internal/reconcile
//     without a "//nostr:allow-poll <reason>" annotation;
//   - exported internal/ symbols whose only callers are tests (B-24).
//
// Regenerate every baseline (Go and web) with:
//
//	make arch-baseline
//
// The package has no non-test code; this file only documents it.
package archtest
