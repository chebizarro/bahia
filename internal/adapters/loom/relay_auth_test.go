package loom

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/khatru"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// authRequiredRelay is an in-process khatru relay holding events that refuses
// unauthenticated REQs with "auth-required:" (and its AUTH challenge). reqs
// counts the REQ filters it was sent.
func authRequiredRelay(t *testing.T, reqs *atomic.Int32, events ...*nostr.Event) string {
	return newAuthRequiredRelay(t, reqs, nil, nil, events...)
}

func newAuthRequiredRelay(t *testing.T, reqs *atomic.Int32, rejectFirst func(nostr.Filter) bool, onListenerAdded func(*khatru.WebSocket, int, string, nostr.Filter), events ...*nostr.Event) string {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	for _, ev := range events {
		if _, err := relay.AddEvent(t.Context(), *ev); err != nil {
			t.Fatal(err)
		}
	}
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		reqs.Add(1)
		first := rejectFirst != nil && rejectFirst(filter)
		if _, ok := khatru.GetAuthed(ctx); first || !ok {
			return true, "auth-required: authenticated clients only"
		}
		return false, ""
	}
	relay.OnListenerAdded = onListenerAdded
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// TestAwaitJobStatusFromWorker_RelayAuthIsThePools: the Loom client has no
// AUTH logic of its own. With a signer, the shared pool
// answers the relay's challenge and reissues both REQs. The fixture refuses
// each filter's first REQ even if AUTH completes between them. The result
// arrives after both authenticated listeners are installed, so completion cannot
// cancel the second re-REQ before it reaches the relay. Without a signer,
// the pool's terminal "auth-required:" CLOSED fails the wait.
func TestAwaitJobStatusFromWorker_RelayAuthIsThePools(t *testing.T) {
	clientSK, clientPK := generatedKeyPair(t)
	workerSK, workerPK := generatedKeyPair(t)
	jobID := strings.Repeat("c", 64)
	var reqs atomic.Int32
	type relayListener struct {
		ws *khatru.WebSocket
		id string
	}
	ready := make(chan struct{})
	resultListener := make(chan relayListener, 1)
	var listeners atomic.Int32
	var firstStatus, firstResult atomic.Bool
	rejectFirst := func(filter nostr.Filter) bool {
		if len(filter.Kinds) != 1 {
			return false
		}
		switch int(filter.Kinds[0]) {
		case KindJobStatus:
			return !firstStatus.Swap(true)
		case KindJobResult:
			return !firstResult.Swap(true)
		default:
			return false
		}
	}
	relayURL := newAuthRequiredRelay(t, &reqs, rejectFirst, func(ws *khatru.WebSocket, _ int, id string, filter nostr.Filter) {
		if len(filter.Kinds) == 1 && int(filter.Kinds[0]) == KindJobResult {
			resultListener <- relayListener{ws, id}
		}
		if listeners.Add(1) == 2 {
			close(ready)
		}
	})
	result := validResultEvent(t, workerSK, jobID, clientPK)
	cfg := config.LoomConfig{JobTimeout: 10 * time.Second}

	t.Run("pool signer", func(t *testing.T) {
		reqs.Store(0)
		pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrAdapter.WithAuthSigner(keyer.NewPlainKeySigner(nostr.MustSecretKeyFromHex(clientSK))))
		defer pool.Close()
		client := NewClient(cfg, testKeyer(clientSK), pool, zap.NewNop())
		type awaitResult struct {
			status *JobStatus
			err    error
		}
		completed := make(chan awaitResult, 1)
		go func() {
			status, err := client.AwaitJobStatusFromWorker(t.Context(), jobID, workerPK)
			completed <- awaitResult{status, err}
		}()
		select {
		case <-ready:
		case got := <-completed:
			t.Fatalf("wait ended before both authenticated REQs: status=%+v error=%v", got.status, got.err)
		}
		// Two filters, each refused once and reissued once by the pool after
		// AUTH: no consumer resubscribe.
		if got := reqs.Load(); got != 4 {
			t.Fatalf("relay saw %d REQ filters, want 4", got)
		}
		listener := <-resultListener
		if err := listener.ws.WriteJSON(nostr.EventEnvelope{SubscriptionID: &listener.id, Event: *result}); err != nil {
			t.Fatalf("relay EVENT write: %v", err)
		}
		got := <-completed
		if got.err != nil || got.status == nil || got.status.Status != StatusCompleted {
			t.Fatalf("status=%+v error=%v, want the result", got.status, got.err)
		}
	})

	t.Run("stored result after auth", func(t *testing.T) {
		var storedReqs atomic.Int32
		storedURL := authRequiredRelay(t, &storedReqs, result)
		pool := nostrAdapter.NewRelayPool([]string{storedURL}, zap.NewNop(), nostrAdapter.WithAuthSigner(keyer.NewPlainKeySigner(nostr.MustSecretKeyFromHex(clientSK))))
		defer pool.Close()
		client := NewClient(cfg, testKeyer(clientSK), pool, zap.NewNop())
		status, err := client.AwaitJobStatusFromWorker(t.Context(), jobID, workerPK)
		if err != nil || status == nil || status.Status != StatusCompleted {
			t.Fatalf("status=%+v error=%v, want the stored result", status, err)
		}
	})

	t.Run("no signer", func(t *testing.T) {
		pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop())
		defer pool.Close()
		client := NewClient(cfg, testKeyer(clientSK), pool, zap.NewNop())
		_, err := client.AwaitJobStatusFromWorker(t.Context(), jobID, workerPK)
		if err == nil || !strings.Contains(err.Error(), "auth-required: authenticated clients only") {
			t.Fatalf("error = %v, want the relay's auth-required reason", err)
		}
	})
}
