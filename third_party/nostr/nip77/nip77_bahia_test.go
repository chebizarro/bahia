package nip77_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/eventstore/wrappers"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip77"
)

// TestNegentropySyncClosesItsConnection (Bahia patch, see BAHIA_PATCHES.md):
// NegentropySync dials its own connection on a background context, so without
// the patch every call left a websocket open. The relay must see the client
// disconnect after a completed sync and after a NEG-ERR, and a late relay frame
// must not wedge the read loop.
func TestNegentropySyncClosesItsConnection(t *testing.T) {
	relay := khatru.NewRelay()
	relay.Negentropy = true
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	relay.UseEventstore(store, 500)
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if khatru.IsNegentropySession(ctx) && len(filter.Authors) == 0 {
			return true, "blocked: an author is required"
		}
		return false, ""
	}
	disconnected := make(chan struct{}, 4)
	relay.OnDisconnect = func(context.Context) { disconnected <- struct{}{} }
	server := httptest.NewServer(relay)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")

	sk := nostr.Generate()
	ev := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "hello"}
	if err := ev.Sign(sk); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.AddEvent(t.Context(), ev); err != nil {
		t.Fatal(err)
	}

	local := wrappers.StorePublisher{Store: &slicestore.SliceStore{}, MaxLimit: 100}
	if err := local.Init(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	waitDisconnect := func(what string) {
		t.Helper()
		select {
		case <-disconnected:
		case <-ctx.Done():
			t.Fatalf("relay never saw the %s sync disconnect", what)
		}
	}

	if err := nip77.NegentropySync(ctx, url, nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}}, nil, local, nip77.SyncEventsFromIDs); err != nil {
		t.Fatalf("sync: %v", err)
	}
	waitDisconnect("completed")
	found := false
	for range local.QueryEvents(nostr.Filter{IDs: []nostr.ID{ev.ID}}) {
		found = true
	}
	if !found {
		t.Fatal("the relay's event was not downloaded")
	}

	err := nip77.NegentropySync(ctx, url, nostr.Filter{Kinds: []nostr.Kind{1}}, nil, local, func(ctx context.Context, dir nip77.Direction) {
		// Items never closes on a refused session; stop with the context.
		select {
		case <-dir.Items:
		case <-ctx.Done():
		}
	})
	if err == nil || !strings.Contains(err.Error(), "NEG-ERR") || !strings.Contains(err.Error(), "an author is required") {
		t.Fatalf("refused sync returned %v, want the relay's NEG-ERR", err)
	}
	cancel()
	waitDisconnectAfterCancel := func() {
		select {
		case <-disconnected:
		case <-time.After(10 * time.Second):
			t.Fatal("relay never saw the refused sync disconnect")
		}
	}
	waitDisconnectAfterCancel()
}
