package identityrecovery

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

func TestCleanupDirectFetchSplitsSaturatedIDBatch(t *testing.T) {
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 3)
	relay.Info.Limitation = &nip11.RelayLimitationDocument{MaxLimit: 3}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	old := signer(t)
	var ids []string
	for i := range 7 {
		ev := nostr.Event{Kind: 1, CreatedAt: 100, Content: fmt.Sprintf("candidate %d", i)}
		if err := old.SignEvent(t.Context(), &ev); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ev.ID.Hex())
	}
	pool := adapter.NewRelayPool([]string{url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool.Connect(ctx)
	source := CleanupRelaySource{Pool: pool, Relays: []string{url}}
	events, err := source.FetchCandidates(ctx, pub(t, old), ids)
	if err != nil || len(events) != len(ids) {
		t.Fatalf("direct ID batches did not recover all candidates: %d, %v", len(events), err)
	}
	parsed := make([]nostr.ID, 0, len(ids))
	for _, raw := range ids {
		id, err := nostr.IDFromHex(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, id)
	}
	accepted, err := (RelaySource{Pool: pool, Relays: []string{url}}).Read(ctx, nostr.Filter{IDs: parsed, Authors: []nostr.PubKey{pub(t, old)}})
	if err != nil || len(accepted) != len(ids) {
		t.Fatalf("accepted-head ID read did not recover all candidates: %d, %v", len(accepted), err)
	}
}
