package nostr

import (
	"os"
	"testing"

	"github.com/openagentsinc/bahia/internal/nostrout"
)

// TestMain isolates this package's tests from the process-wide outbound
// budget: dozens of unrelated cases publish through relay pools, and a shared
// production budget would make them order-dependent. Admission behavior is
// still exercised for real; tests that assert budgets inject tight controllers.
func TestMain(m *testing.M) {
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	shared := nostrout.New(nostrout.Config{
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
	})
	defaultOutboundAdmission = func() *nostrout.Admission { return shared }
	os.Exit(m.Run())
}
