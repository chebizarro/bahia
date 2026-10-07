package signet

import (
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// generousTestAdmission keeps unrelated Signet tests from sharing the
// process-wide production budget, which would make them order-dependent.
// Admission behavior is still exercised for real; tests that assert budgets
// inject tight controllers.
func generousTestAdmission() *nostrout.Admission {
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	return nostrout.New(nostrout.Config{
		Aggregate: generous,
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: generous,
			nostrout.PurposeState:    generous,
			nostrout.PurposeGeneral:  generous,
			nostrout.PurposeBulk:     generous,
			nostrout.PurposeSigner:   generous,
		},
		RelayWire:             generous,
		RelayWirePriority:     generous,
		MaxActivePublications: 100_000,
		MaxRelayIdentities:    100_000,
		MaxOpaqueWaiters:      100_000,
	})
}
