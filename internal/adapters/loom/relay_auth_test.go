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
	"fiatjaf.com/nostr/khatru"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// authRequiredRelay is an in-process khatru relay holding events that refuses
// unauthenticated REQs with "auth-required:" (and its AUTH challenge). reqs
// counts the REQ filters it was sent.
func authRequiredRelay(t *testing.T, reqs *atomic.Int32, events ...*nostr.Event) string {
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
	relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
		reqs.Add(1)
		if _, ok := khatru.GetAuthed(ctx); !ok {
			return true, "auth-required: authenticated clients only"
		}
		return false, ""
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// TestAwaitJobStatusFromWorker_RelayAuthIsThePools: the Loom client has no
// AUTH logic of its own (bahia-irsry.47). With a signer, the shared pool
// answers the relay's challenge and reissues the REQ, and the stored result
// completes the wait on the client's only subscription. Without one, the
// pool's terminal "auth-required:" CLOSED fails the wait.
func TestAwaitJobStatusFromWorker_RelayAuthIsThePools(t *testing.T) {
	clientSK, clientPK := generatedKeyPair(t)
	workerSK, workerPK := generatedKeyPair(t)
	jobID := strings.Repeat("c", 64)
	var reqs atomic.Int32
	relayURL := authRequiredRelay(t, &reqs, validResultEvent(t, workerSK, jobID, clientPK))
	cfg := config.LoomConfig{JobTimeout: 10 * time.Second}

	t.Run("pool signer", func(t *testing.T) {
		reqs.Store(0)
		pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrAdapter.WithPrivateKey(clientSK))
		defer pool.Close()
		client := NewClient(cfg, clientSK, pool, zap.NewNop())
		status, err := client.AwaitJobStatusFromWorker(t.Context(), jobID, workerPK)
		if err != nil || status.Status != StatusCompleted {
			t.Fatalf("status=%+v error=%v, want the stored result", status, err)
		}
		// Two filters, each refused once and reissued once by the pool after
		// AUTH: no consumer resubscribe.
		if got := reqs.Load(); got != 4 {
			t.Fatalf("relay saw %d REQ filters, want 4", got)
		}
	})

	t.Run("no signer", func(t *testing.T) {
		pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop())
		defer pool.Close()
		client := NewClient(cfg, clientSK, pool, zap.NewNop())
		_, err := client.AwaitJobStatusFromWorker(t.Context(), jobID, workerPK)
		if err == nil || !strings.Contains(err.Error(), "auth-required: authenticated clients only") {
			t.Fatalf("error = %v, want the relay's auth-required reason", err)
		}
	})
}
