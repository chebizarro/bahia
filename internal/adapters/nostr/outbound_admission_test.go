package nostr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newIsolatedTestAdmission keeps unrelated pool tests from sharing the
// process-wide budget while still exercising real admission.
func newIsolatedTestAdmission() *nostrout.Admission {
	generous := nostrout.PurposeBudget{RatePerMinute: 60_000, Burst: 10_000}
	return nostrout.New(nostrout.Config{
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
		MaxActivePublications: 10_000,
	})
}

func connectedTestPool(t *testing.T, admission *nostrout.Admission, urls ...string) *RelayPool {
	t.Helper()
	pool := NewRelayPool(urls, zap.NewNop(), WithOutboundAdmission(admission))
	for _, url := range pool.URLs() {
		pool.relays[url] = &managedRelay{
			url:       url,
			relay:     gonostr.NewRelay(context.Background(), url, gonostr.RelayOptions{}),
			connected: true,
		}
	}
	return pool
}

func TestRelayPoolsDefaultToOneProcessWideAdmission(t *testing.T) {
	one := NewRelayPool(nil, zap.NewNop())
	two := NewRelayPool(nil, zap.NewNop(), WithOutboundAdmission(nil))
	require.Same(t, defaultOutboundAdmission(), one.outboundAdmission)
	require.Same(t, one.outboundAdmission, two.outboundAdmission, "nil injection must keep the shared default, never disable admission")
}

func TestWithOutboundAdmissionSharesBudgetAcrossPools(t *testing.T) {
	admission := nostrout.New(nostrout.Config{RatePerMinute: 1, Burst: 1})
	one := connectedTestPool(t, admission, "wss://one.example")
	two := connectedTestPool(t, admission, "wss://two.example")
	var frames atomic.Int32
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		frames.Add(1)
		return nil
	})

	_, err := one.Publish(t.Context(), gonostr.Event{Kind: 1})
	require.NoError(t, err)
	_, err = two.Publish(t.Context(), gonostr.Event{Kind: 1})
	require.True(t, errors.Is(err, nostrout.ErrBudgetExceeded), "second pool must share first pool's exhausted budget: %v", err)
	require.Equal(t, int32(1), frames.Load(), "rejection must happen before relay I/O")
}

func TestRelayPoolRejectedPublicationSendsNoFrame(t *testing.T) {
	admission := newIsolatedTestAdmission()
	pool := connectedTestPool(t, admission, "wss://relay.example")
	path := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	killed := nostrout.New(nostrout.Config{KillSwitchFile: path})
	pool.outboundAdmission = killed
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		t.Fatal("kill switch must prevent every EVENT frame")
		return nil
	})
	_, err := pool.PublishWithResults(t.Context(), gonostr.Event{Kind: 5})
	require.ErrorIs(t, err, nostrout.ErrKillSwitch)
}

func TestRelayPoolRateLimitOpensCircuitForEveryPool(t *testing.T) {
	admission := newIsolatedTestAdmission()
	limited := connectedTestPool(t, admission, "wss://limited.example")
	other := connectedTestPool(t, admission, "wss://other.example")
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		return errors.New("msg: rate-limited: slow down")
	})
	results, err := limited.PublishWithResults(t.Context(), gonostr.Event{Kind: 1})
	require.Error(t, err)
	require.True(t, results[0].IsRateLimited())

	_, err = other.PublishWithResults(t.Context(), gonostr.Event{Kind: 5})
	require.ErrorIs(t, err, nostrout.ErrCircuitOpen)
	require.Equal(t, uint64(1), admission.Metrics().RelayRateLimited)
}

func TestRelayPoolReconnectReplaySuppressesAcceptedDestinationsOnly(t *testing.T) {
	admission := newIsolatedTestAdmission()
	pool := connectedTestPool(t, admission, "wss://a.example", "wss://b.example")
	setConnectRelayForTest(t, pool, func(_ context.Context, url string, _ gonostr.RelayOptions) (*gonostr.Relay, error) {
		return gonostr.NewRelay(context.Background(), url, gonostr.RelayOptions{}), nil
	})
	var sentMu sync.Mutex
	sent := map[string]int{}
	setPublishOnRelayForTest(t, func(relay *gonostr.Relay, _ context.Context, _ gonostr.Event) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent[relay.URL]++
		if relay.URL == "wss://b.example" && sent[relay.URL] == 1 {
			return errors.New("connection reset")
		}
		return nil
	})
	ev := gonostr.Event{Kind: 1, Content: "replayed", CreatedAt: 1}
	require.NoError(t, ev.Sign(gonostr.Generate()))

	_, err := pool.PublishWithResults(t.Context(), ev)
	require.NoError(t, err)
	results, err := pool.PublishWithResults(t.Context(), ev)
	require.NoError(t, err)
	sentMu.Lock()
	defer sentMu.Unlock()
	require.Equal(t, 1, sent["wss://a.example"], "accepted destination must not be resent on replay")
	require.Equal(t, 2, sent["wss://b.example"], "failed destination remains eligible")
	require.Len(t, results, 2)
	require.Equal(t, 2, countSuccessfulPublishResults(results))
}

// TestRelayFlappingCannotExceedWireBudget: a relay whose transport keeps
// dying forces a reconnect before every frame, but the per-relay wire budget
// still bounds how many EVENT frames reach it — reconnects cannot accumulate
// permits that later send together.
func TestRelayFlappingCannotExceedWireBudget(t *testing.T) {
	tight := nostrout.New(nostrout.Config{
		Aggregate:      nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		PurposeBudgets: generousAdmissionLanes(),
		RelayWire:      nostrout.PurposeBudget{RatePerMinute: 6, Burst: 2},
	})
	pool := connectedTestPool(t, tight, "wss://flap.example")
	setConnectRelayForTest(t, pool, func(_ context.Context, url string, _ gonostr.RelayOptions) (*gonostr.Relay, error) {
		return gonostr.NewRelay(context.Background(), url, gonostr.RelayOptions{}), nil
	})
	var frames atomic.Int32
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		frames.Add(1)
		return errors.New("connection reset")
	})

	var lastErr error
	for i := range 5 {
		ev := gonostr.Event{Kind: 1, Content: fmt.Sprintf("flap-%d", i), CreatedAt: gonostr.Timestamp(i + 1)}
		require.NoError(t, ev.Sign(gonostr.Generate()))
		results, err := pool.PublishWithResults(t.Context(), ev)
		if err != nil && len(results) == 0 {
			lastErr = err
		} else if len(results) == 1 && results[0].Error != nil {
			lastErr = results[0].Error
		}
	}
	require.Equal(t, int32(2), frames.Load(), "wire budget must bound frames across the flapping reconnects")
	require.True(t, errors.Is(lastErr, nostrout.ErrBudgetExceeded), "later publishes must fail on the wire budget: %v", lastErr)
}

// TestConcurrentPoolPublishersCannotExceedSharedBudget: many publishers on
// many pools racing one controller never send more frames in aggregate than
// the burst allows, and never more than the per-relay wire burst to one relay.
func TestConcurrentPoolPublishersCannotExceedSharedBudget(t *testing.T) {
	shared := nostrout.New(nostrout.Config{
		Aggregate:      nostrout.PurposeBudget{RatePerMinute: 60, Burst: 4},
		PurposeBudgets: generousAdmissionLanes(),
		RelayWire:      nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
	})
	pool := connectedTestPool(t, shared, "wss://shared.example")
	var frames atomic.Int32
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		frames.Add(1)
		return nil
	})

	const publishers = 16
	var wg sync.WaitGroup
	for i := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := gonostr.Event{Kind: 1, Content: fmt.Sprintf("racer-%d", i), CreatedAt: gonostr.Now()}
			if err := ev.Sign(gonostr.Generate()); err != nil {
				return
			}
			_, _ = pool.PublishWithResults(context.Background(), ev)
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, frames.Load(), int32(4), "aggregate burst bounds every concurrent publisher together")
	require.Greater(t, frames.Load(), int32(0))
	require.LessOrEqual(t, shared.Metrics().Admitted, uint64(4))
}
