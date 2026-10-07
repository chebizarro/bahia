package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/coder/websocket"
)

// closedBurstRelay is a minimal NIP-01 relay that answers every REQ with
// EOSE, a burst of live EVENTs, and then CLOSED for the same subscription —
// the frame sequence the relay sidecar emits when a subscriber overflows its
// queue. The EVENTs are post-EOSE, so the client dispatches
// them on goroutines that are not covered by the stored-event wait group and
// are still in flight when CLOSED tears the subscription down.
func closedBurstRelay(t *testing.T, burst int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(msg, &frame) != nil || len(frame) < 2 {
				continue
			}
			var verb, subID string
			_ = json.Unmarshal(frame[0], &verb)
			_ = json.Unmarshal(frame[1], &subID)
			if verb != "REQ" {
				continue
			}
			frames := make([]string, 0, burst+2)
			frames = append(frames, fmt.Sprintf(`["EOSE",%q]`, subID))
			for i := range burst {
				frames = append(frames, fmt.Sprintf(
					`["EVENT",%q,{"id":"%064x","pubkey":"%064x","created_at":%d,"kind":1,"tags":[],"content":"e%d","sig":"%0128x"}]`,
					subID, i+1, 1, time.Now().Unix(), i, 0))
			}
			frames = append(frames, fmt.Sprintf(`["CLOSED",%q,"error: subscriber queue overflow"]`, subID))
			for _, f := range frames {
				if err := conn.Write(ctx, websocket.MessageText, []byte(f)); err != nil {
					return
				}
			}
		}
	}))
}

// TestLibrarySubscriptionClosedWhileEventsInFlight is the regression test for
// in the unpatched fiatjaf.com/nostr, Subscription.dispatchEvent
// sends on sub.Events while the CLOSED-triggered teardown closes that channel,
// which the race detector reports and which can panic with "send on closed
// channel". Run with -race.
func TestLibrarySubscriptionClosedWhileEventsInFlight(t *testing.T) {
	const (
		rounds = 40
		burst  = 64
	)
	srv := closedBurstRelay(t, burst)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	relay, err := gonostr.RelayConnect(ctx, url, gonostr.RelayOptions{AssumeValid: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer relay.Close()

	for round := range rounds {
		sub, err := relay.Subscribe(ctx, gonostr.Filter{Kinds: []gonostr.Kind{1}}, gonostr.SubscriptionOptions{
			Label: fmt.Sprintf("race%d", round),
		})
		if err != nil {
			t.Fatalf("round %d subscribe: %v", round, err)
		}

		// Consume a few events concurrently (like a real subscriber), then
		// stop reading so the rest stay parked on the unbuffered channel.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < burst/4; i++ {
				select {
				case _, ok := <-sub.Events:
					if !ok {
						return
					}
				case <-sub.Context.Done():
					return
				}
			}
		}()

		select {
		case reason := <-sub.ClosedReason:
			if !strings.Contains(reason, "overflow") {
				t.Fatalf("round %d: unexpected CLOSED reason %q", round, reason)
			}
		case <-ctx.Done():
			t.Fatalf("round %d: CLOSED never delivered", round)
		}
		select {
		case <-sub.Context.Done():
		case <-ctx.Done():
			t.Fatalf("round %d: subscription not torn down after CLOSED", round)
		}
		wg.Wait()
		// Events must be closed exactly once and drain to completion.
		for range sub.Events {
		}
	}
}
