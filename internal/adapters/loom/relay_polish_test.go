package loom

import (
	"context"
	"errors"
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
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// refusingRelay is an in-process khatru relay that CLOSEs every REQ with
// reason; reqs counts the REQ filters it was sent.
func refusingRelay(t *testing.T, reqs *atomic.Int32, reason string) string {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	relay.OnRequest = func(context.Context, nostr.Filter) (bool, string) {
		reqs.Add(1)
		return true, reason
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// eoseFirstPool hides a merged subscription's Closed and Events channels, so
// the aggregate EndOfStoredEvents is the only signal the plane client gets:
// the select ordering in which the EOSE branch wins over a terminal CLOSED
// that arrived with it.
type eoseFirstPool struct{ *nostrAdapter.RelayPool }

func (p eoseFirstPool) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	sub, err := p.RelayPool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	shadow := *sub
	shadow.Closed = nil
	shadow.Events = make(chan *nostr.Event)
	return &shadow, nil
}

// TestPlaneObserveDoesNotRetryATerminalClosedThatArrivesWithEOSE: when a
// relay's terminal CLOSED and the aggregate EOSE it causes arrive together,
// Observe must give up whichever it handles first (bahia-irsry.49). The pool
// records the terminal CLOSED before EndOfStoredEvents closes, so the EOSE
// branch sees the give-up instead of reconnecting.
func TestPlaneObserveDoesNotRetryATerminalClosedThatArrivesWithEOSE(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(*nostrAdapter.RelayPool) PlaneRelayPool
	}{
		{name: "EOSE handled first", wrap: func(p *nostrAdapter.RelayPool) PlaneRelayPool { return eoseFirstPool{p} }},
		{name: "any order", wrap: func(p *nostrAdapter.RelayPool) PlaneRelayPool { return p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, p, _, _, now := planeFixture(t)
			var reqs atomic.Int32
			relayURL := refusingRelay(t, &reqs, "blocked: not on the allowlist")
			pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop())
			t.Cleanup(pool.Close)
			clientKey, _ := generatedKeyPair(t)
			client, err := NewPlaneClient(tc.wrap(pool), HexKeyCanonicalSigner{PrivateKey: clientKey}, []domain.ExecutionPlaneEndpoint{e})
			require.NoError(t, err)
			client.now = func() time.Time { return now }
			client.backoff = time.Millisecond
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			var unavailable atomic.Int32
			err = client.Observe(ctx, e, p.ID, planeRecordingObserver{
				unavailable: func() error { unavailable.Add(1); return nil },
				eose:        func() error { return errors.New("no EOSE expected from a refusing relay") },
			})
			require.NoError(t, ctx.Err(), "Observe kept retrying a terminal CLOSED until the deadline")
			require.ErrorContains(t, planeCause(t, err), "blocked: not on the allowlist")
			require.Equal(t, int32(1), reqs.Load(), "the refused REQ is never sent again")
			require.Equal(t, int32(1), unavailable.Load(), "eligibility retracted once")
		})
	}
}

// TestAwaitJobStatusFromWorker_StopsWhenThePoolGivesUp: once the pool has
// given up on every relay (here a retryable CLOSED past the retry budget),
// the job wait fails with the give-up instead of resubscribing, which would
// restart the refusals with a fresh budget until the job timeout.
func TestAwaitJobStatusFromWorker_StopsWhenThePoolGivesUp(t *testing.T) {
	clientSK, _ := generatedKeyPair(t)
	_, workerPK := generatedKeyPair(t)
	var reqs atomic.Int32
	relayURL := refusingRelay(t, &reqs, "error: overloaded")
	pool := nostrAdapter.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrAdapter.WithRetryableClosedBudget(1))
	defer pool.Close()
	cfg := config.LoomConfig{JobTimeout: 10 * time.Second}
	client := NewClient(cfg, clientSK, pool, zap.NewNop())
	client.jobSubscriptionBackoff = time.Millisecond
	started := time.Now()

	_, err := client.AwaitJobStatusFromWorker(t.Context(), strings.Repeat("d", 64), workerPK)
	require.ErrorIs(t, err, nostrAdapter.ErrSubscriptionGaveUp)
	require.ErrorContains(t, err, "error: overloaded")
	require.Less(t, time.Since(started), cfg.JobTimeout, "the wait ended on the give-up, not the job timeout")
	require.Equal(t, int32(4), reqs.Load(), "two filters, each sent once and reissued once by the pool; no resubscribe")
}
