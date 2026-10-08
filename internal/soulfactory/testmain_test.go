package soulfactory

import (
	"os"
	"testing"

	"github.com/openagentsinc/bahia/internal/nostrout"
)

// TestMain isolates this package's tests from the production outbound
// admission budgets: reactors and relay clients built without an explicit
// controller share the process-wide nostrout.Default, whose per-process
// budgets a test binary can exhaust and turn into order-dependent refusals
// (see generousTestAdmissionConfig and internal/adapters/nostr/main_test.go).
func TestMain(m *testing.M) {
	nostrout.InitDefault(generousTestAdmissionConfig())
	os.Exit(m.Run())
}
