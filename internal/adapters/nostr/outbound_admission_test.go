package nostr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newIsolatedTestAdmission keeps unrelated pool tests from sharing the
// process-wide budget while still exercising real admission.
func newIsolatedTestAdmission() *OutboundAdmission {
	generous := OutboundPurposeBudget{RatePerMinute: 60_000, Burst: 10_000}
	return NewOutboundAdmission(OutboundAdmissionConfig{
		Aggregate: generous,
		PurposeBudgets: map[OutboundPurpose]OutboundPurposeBudget{
			OutboundPurposePriority: generous,
			OutboundPurposeState:    generous,
			OutboundPurposeGeneral:  generous,
			OutboundPurposeBulk:     generous,
			OutboundPurposeSigner:   generous,
		},
		RelayWire:             generous,
		RelayWirePriority:     generous,
		MaxActivePublications: 10_000,
	})
}

func connectedTestPool(t *testing.T, admission *OutboundAdmission, urls ...string) *RelayPool {
	t.Helper()
	pool := NewRelayPool(urls, zap.NewNop(), WithOutboundAdmission(admission))
	pool.isRelayConnected = func(relay *gonostr.Relay) bool { return relay != nil }
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
	require.Same(t, nostrout.Default(), one.outboundAdmission)
	require.Same(t, one.outboundAdmission, two.outboundAdmission, "nil injection must keep the shared default, never disable admission")
	require.Same(t, nostrout.Default(), DefaultOutboundAdmission())
}

func TestWithOutboundAdmissionSharesBudgetAcrossPools(t *testing.T) {
	admission := NewOutboundAdmission(OutboundAdmissionConfig{RatePerMinute: 1, Burst: 1})
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
	require.True(t, errors.Is(err, ErrOutboundBudgetExceeded), "second pool must share first pool's exhausted budget: %v", err)
	require.Equal(t, int32(1), frames.Load(), "rejection must happen before relay I/O")
}

func TestRelayPoolRejectedPublicationSendsNoFrame(t *testing.T) {
	admission := newIsolatedTestAdmission()
	pool := connectedTestPool(t, admission, "wss://relay.example")
	path := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	killed := NewOutboundAdmission(OutboundAdmissionConfig{KillSwitchFile: path})
	pool.outboundAdmission = killed
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		t.Fatal("kill switch must prevent every EVENT frame")
		return nil
	})
	_, err := pool.PublishWithResults(t.Context(), gonostr.Event{Kind: 5})
	require.ErrorIs(t, err, ErrOutboundKillSwitch)
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
	require.ErrorIs(t, err, ErrOutboundCircuitOpen)
	require.Equal(t, uint64(1), limited.OutboundAdmissionMetrics().RelayRateLimited)
}

func TestRelayPoolReconnectReplaySuppressesAcceptedDestinationsOnly(t *testing.T) {
	admission := newIsolatedTestAdmission()
	pool := connectedTestPool(t, admission, "wss://a.example", "wss://b.example")
	setConnectRelayForTest(t, pool, func(_ context.Context, url string, _ gonostr.RelayOptions) (*gonostr.Relay, error) {
		return gonostr.NewRelay(context.Background(), url, gonostr.RelayOptions{}), nil
	})
	sent := map[string]int{}
	setPublishOnRelayForTest(t, func(relay *gonostr.Relay, _ context.Context, _ gonostr.Event) error {
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
	require.Equal(t, 1, sent["wss://a.example"], "accepted destination must not be resent on replay")
	require.Equal(t, 2, sent["wss://b.example"], "failed destination remains eligible")
	require.Len(t, results, 2)
	require.Equal(t, 2, countSuccessfulPublishResults(results))
}
