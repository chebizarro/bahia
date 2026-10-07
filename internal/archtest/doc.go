// Package archtest holds Bahia's architecture ratchet tests.
//
// Each gate walks the module's Go sources with go/packages type information
// and compares what it finds against a checked-in baseline under testdata/.
// A gate fails only when a file gains violations beyond its baseline, so the
// existing debt is recorded and can only shrink. The gates are:
//
//   - legacy kind constants outside internal/nostrmigration;
//   - direct Subscribe/Fetch calls on fiatjaf.com/nostr Relay or Pool objects
//     outside the relay pool and SoulFactory bus;
//   - time.NewTicker/time.Tick in internal/service and internal/reconcile
//     without a "//nostr:allow-poll <reason>" annotation;
//   - exported internal/ symbols whose only callers are tests.
//
// Regenerate every baseline (Go and web) with:
//
//	make arch-baseline
//
// The package has no non-test code; this file only documents it.
package archtest
