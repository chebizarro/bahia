package nostr

import (
	"context"
	"sync"
	"testing"
)

// TestOKCallbacksResetOnCloseIsRaceFree (Bahia patch, see BAHIA_PATCHES.md):
// publish registers its OK callback under okCallbacksMutex, and every publish
// waiting when the connection closes resets okCallbacks. That reset ran
// without the mutex, racing the other publishes' registrations. The relay is
// never dialed (an undialed relay accepts writes without sending them), so
// each publish waits on the connection context until cancel closes it. Run
// with -race; the unlocked reset reports a data race here.
func TestOKCallbacksResetOnCloseIsRaceFree(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		relay := NewRelay(ctx, "wss://unused.example", RelayOptions{})
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				id := ID{byte(i)}
				_ = relay.publish(context.Background(), id, &EventEnvelope{})
			})
		}
		cancel()
		wg.Wait()
	}
}
