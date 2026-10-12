package nostr

import (
	"context"
	"errors"
	"sync"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	guardControlledRelay = "wss://relay.sharegap.net"
	guardPublicRelay     = "wss://relay.damus.io"
)

// frameRecorder answers every EVENT frame with OK true and records which
// relays received one.
type frameRecorder struct {
	mu     sync.Mutex
	relays []string
	fail   map[string]error
}

func (f *frameRecorder) publish(relay *gonostr.Relay, _ context.Context, _ gonostr.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relays = append(f.relays, relay.URL)
	return f.fail[relay.URL]
}

func (f *frameRecorder) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.relays...)
}

func guardedPublisher(t *testing.T, secret gonostr.SecretKey, quorum int, pool *RelayPool, opts ...PublisherOption) *Publisher {
	t.Helper()
	opts = append([]PublisherOption{WithLocalOutbox(openDeliveryTestOutbox(t), nil)}, opts...)
	return newKeyedTestPublisher(config.NostrConfig{PrivateKey: secret.Hex(), PublishEnabled: true, PublishQuorum: quorum}, pool, nil, zap.NewNop(), opts...)
}

// The 2026-10-11 production topology: the write set held a public relay and a
// quorum of one. The guard keeps the service's cp-state off the public relay
// even though the pool still lists it.
func TestRelayGuardKeepsServiceControlPlaneOffPublicRelays(t *testing.T) {
	secret := gonostr.Generate()
	guard, err := relayguard.New(secret.Public(), []string{guardControlledRelay})
	require.NoError(t, err)
	pool := connectedTestPool(t, newIsolatedTestAdmission(), guardControlledRelay, guardPublicRelay)
	pool.relayGuard = guard
	frames := &frameRecorder{}
	setPublishOnRelayForTest(t, frames.publish)
	publisher := guardedPublisher(t, secret, 1, pool)

	results, err := publisher.PublishSignedEventWithResults(t.Context(), &gonostr.Event{Kind: kinds.CASControlState, Tags: gonostr.Tags{{"d", "service:x"}}, Content: "{}"})
	require.NoError(t, err)
	require.Equal(t, []string{guardControlledRelay}, frames.sent(), "no EVENT frame may reach the public relay")
	var blocked *PublishResult
	for i := range results {
		if results[i].RelayURL == guardPublicRelay {
			blocked = &results[i]
		}
	}
	require.NotNil(t, blocked, "the refused relay must be reported")
	require.True(t, IsBlockedReason(blocked.Reason), "refusal reason %q must be a blocked: rejection", blocked.Reason)

	// A non-control-plane kind is not the guard's concern.
	_, err = pool.PublishWithResults(t.Context(), gonostr.Event{PubKey: secret.Public(), Kind: kinds.ContextVMGiftWrap})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{guardControlledRelay, guardControlledRelay, guardPublicRelay}, frames.sent())
}

// Quorum is counted over the publisher's write relays only: a Loom relay that
// shares the pool can neither receive the event nor satisfy the quorum.
func TestPublisherWriteRelaysBoundQuorumAccounting(t *testing.T) {
	const (
		writeA = "wss://bahia.example/relay"
		writeB = "wss://relay.example"
		loom   = "wss://loom.example"
	)
	secret := gonostr.Generate()
	pool := connectedTestPool(t, newIsolatedTestAdmission(), writeA, writeB, loom)
	frames := &frameRecorder{fail: map[string]error{writeB: errors.New("connection reset")}}
	setPublishOnRelayForTest(t, frames.publish)
	publisher := guardedPublisher(t, secret, 2, pool, WithPublisherWriteRelays(func() []string { return []string{writeA, writeB} }))

	_, err := publisher.PublishSignedEventWithResults(t.Context(), &gonostr.Event{Kind: kinds.CASControlState, Tags: gonostr.Tags{{"d", "service:y"}}, Content: "{}"})
	require.ErrorIs(t, err, ErrPublishIncomplete, "one of two write relays accepted; quorum 2 is not met")
	require.ElementsMatch(t, []string{writeA, writeB}, frames.sent())
}

func TestRelayGuardRefusesNegentropyUploadOutsideControlledRelays(t *testing.T) {
	secret := gonostr.Generate()
	guard, err := relayguard.New(secret.Public(), []string{guardControlledRelay})
	require.NoError(t, err)
	pool := NewRelayPool(nil, zap.NewNop(), WithControlPlaneRelayGuard(guard))
	frames := &frameRecorder{}
	setPublishOnRelayForTest(t, frames.publish)
	relay := gonostr.NewRelay(context.Background(), guardPublicRelay, gonostr.RelayOptions{})

	err = pool.publishAdmittedToSessionRelay(t.Context(), nil, relay, gonostr.Event{PubKey: secret.Public(), Kind: kinds.CASAudit})
	var refused *relayguard.RefusedError
	require.ErrorAs(t, err, &refused)
	require.Empty(t, frames.sent())
}
