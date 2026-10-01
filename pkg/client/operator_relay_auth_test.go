package client

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

// TestOperatorRelayAuthIsThePools: the operator client has no AUTH logic of
// its own (bahia-irsry.47). Against a relay that refuses unauthenticated REQs
// and EVENTs, the default RelayPool transport answers the relay's NIP-42
// challenge, reissues the reply REQ on that relay and authenticates the
// publish. The request is published once and its reply completes it.
func TestOperatorRelayAuthIsThePools(t *testing.T) {
	requestKey := nostr.Generate()
	replyKey := nostr.Generate()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	var refusedREQs atomic.Int32
	relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
		if _, ok := khatru.GetAuthed(ctx); !ok {
			refusedREQs.Add(1)
			return true, "auth-required: authenticated clients only"
		}
		return false, ""
	}
	relay.OnEvent = func(ctx context.Context, ev nostr.Event) (bool, string) {
		if _, ok := khatru.GetAuthed(ctx); !ok && ev.PubKey != replyKey.Public() {
			return true, "auth-required: authenticated writers only"
		}
		return false, ""
	}
	// ContextVM requests are ephemeral: this hook stands in for the server
	// listening for them.
	requests := make(chan nostr.Event, 4)
	relay.OnEphemeralEvent = func(_ context.Context, ev nostr.Event) {
		if ev.PubKey == requestKey.Public() {
			requests <- ev
		}
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")

	pool := nostrpool.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrpool.WithPrivateKey(requestKey.Hex()))
	client := newTestOperatorClient(t, requestKey.Hex(), &relayPoolOperatorTransport{pool: pool})
	t.Cleanup(pool.Close)
	client.relays = []string{relayURL}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	type outcome struct {
		result *RuntimeActionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := client.RestartServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
		done <- outcome{result, err}
	}()

	var request nostr.Event
	select {
	case request = <-requests:
	case got := <-done:
		t.Fatalf("request ended before it was published: %v", got.err)
	case <-ctx.Done():
		t.Fatal("the request never reached the auth-required relay")
	}
	reply := signedContextVMResult(t, replyKey.Hex(), request, map[string]any{"action": "restart", "service_id": "svc-1", "environment_id": "env-1"})
	if !reply.Kind.IsEphemeral() {
		t.Fatalf("reply kind %d is not ephemeral; store it before broadcasting", reply.Kind)
	}
	if n := relay.BroadcastEvent(*reply); n == 0 {
		t.Fatal("no live reply subscription on the relay")
	}

	select {
	case got := <-done:
		if got.err != nil || got.result == nil || got.result.Action != "restart" {
			t.Fatalf("result=%+v error=%v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("the reply never completed the request")
	}
	if extra := len(requests); extra != 0 {
		t.Fatalf("request republished %d more time(s)", extra)
	}
	if refusedREQs.Load() == 0 {
		t.Fatal("the relay never refused an unauthenticated REQ; the test did not exercise AUTH")
	}
}
