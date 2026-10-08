package client

import (
	"os"
	"testing"

	"github.com/openagentsinc/bahia/internal/nostrout"
)

// TestMain isolates this package's tests from the production outbound
// admission budgets: tests here publish through relay pools that share the
// process-wide controller, whose per-process budgets a test binary can
// exhaust and turn into order-dependent refusals.
func TestMain(m *testing.M) {
	nostrout.InitDefault(generousTestAdmission())
	os.Exit(m.Run())
}

// generousTestAdmission is this test binary's process-wide outbound admission
// controller, mirroring internal/adapters/nostr/main_test.go: production
// budgets are per-process (priority lane 10/min burst 4, aggregate 45/min
// burst 15), and one test binary stacks the publications of many simulated
// agents, clients and restarts on the shared nostrout.Default. On a slow CI
// host an earlier test — or a ContextVM result retry after a late OK — spends
// the whole burst, and a later test's gift wrap is refused fail-fast before
// any relay I/O (cmd/bahia-dns-agent surfaced this as "request wrap was not
// stored"). Tests that assert admission behaviour inject tight isolated
// controllers (nostrout.New) into the gateways under test and are unaffected.
func generousTestAdmission() nostrout.Config {
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	return nostrout.Config{
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
	}
}
