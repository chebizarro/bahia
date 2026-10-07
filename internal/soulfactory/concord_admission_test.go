package soulfactory

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

func concordAdmissionEvent(t *testing.T, kind nostr.Kind) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: t.Name()}
	require.NoError(t, event.Sign(nostr.Generate()))
	return event
}

func generousConcordWire() nostrout.PurposeBudget {
	return nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
}

// TestConcordPublicationsArePacedByOperationInsteadOfRejected proves the bulk
// operation keeps a multi-event Concord sequence atomic in admission terms:
// ungoverned gift wraps exhaust the reserved priority lane and fail fast,
// while a declared operation paces the same wraps through the bulk lane and
// refuses publications beyond the declared bound.
func TestConcordPublicationsArePacedByOperationInsteadOfRejected(t *testing.T) {
	admission := nostrout.New(nostrout.Config{
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: {RatePerMinute: 1, Burst: 1},
			nostrout.PurposeBulk:     {RatePerMinute: 1_200, Burst: 1},
		},
		RelayWire:         generousConcordWire(),
		RelayWirePriority: generousConcordWire(),
	})
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishFn = func(_ context.Context, event nostr.Event) RelayPublishResult {
		return RelayPublishResult{RelayURL: endpoint.url, Accepted: true}
	}
	client, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayAdmission(admission))
	require.NoError(t, err)
	relays := []string{endpoint.url}
	ctx := context.Background()

	// Without an operation, a burst of gift wraps exhausts the priority lane:
	// this is exactly how a naive hook broke multi-event rotations.
	require.NoError(t, publishConcordInvite(ctx, client, relays, concordAdmissionEvent(t, 1059)))
	err = publishConcordInvite(ctx, client, relays, concordAdmissionEvent(t, 1059))
	require.True(t, errors.Is(err, nostrout.ErrBudgetExceeded), "second ungoverned gift wrap error = %v", err)

	op, err := client.outbound().BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: 5})
	require.NoError(t, err)
	defer op.Close()
	opCtx := nostrout.WithOperation(ctx, op)
	for i := range 5 {
		require.NoError(t, publishConcordInvite(opCtx, client, relays, concordAdmissionEvent(t, 1059)),
			"operation publication %d must be paced, not refused", i)
	}
	err = publishConcordInvite(opCtx, client, relays, concordAdmissionEvent(t, 1059))
	require.True(t, errors.Is(err, nostrout.ErrOperation), "publication beyond the declared bound error = %v", err)

	endpoint.mu.Lock()
	frames := endpoint.publishCalls
	endpoint.mu.Unlock()
	require.Equal(t, 6, frames)
	require.Equal(t, uint64(6), admission.Metrics().Admitted)
}

// TestConcordAuthRetryIsAdmittedAsSecondFrame: a gift wrap uses the priority
// wire share, so a NIP-42 AUTH retry — a second EVENT frame — needs its own
// admission and is refused when the reserved share is spent.
func TestConcordAuthRetryIsAdmittedAsSecondFrame(t *testing.T) {
	admission := nostrout.New(nostrout.Config{
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 1, Burst: 1},
	})
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishResults = []RelayPublishResult{{Reason: "auth-required: sign in"}}
	signer := newFakeSigner(t)
	client, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, WithRelaySigner(signer), withRelayAdmission(admission))
	require.NoError(t, err)

	err = publishConcordInvite(context.Background(), client, []string{endpoint.url}, concordAdmissionEvent(t, 1059))
	require.Error(t, err)

	endpoint.mu.Lock()
	calls := endpoint.publishCalls
	endpoint.mu.Unlock()
	require.Equal(t, 1, calls, "the AUTH retry must not reach the wire without its own admission")
	select {
	case <-endpoint.authCalls:
	default:
		t.Fatal("expected the relay challenge to be answered before the refused retry")
	}
}

// TestConcordReplayOfAcceptedWrapSendsNoFrame: the receipt cache answers a
// replay of an already-accepted wrap locally, so a resumed rotation cannot
// double-send what a relay already holds.
func TestConcordReplayOfAcceptedWrapSendsNoFrame(t *testing.T) {
	admission := nostrout.New(nostrout.Config{
		RelayWire:         generousConcordWire(),
		RelayWirePriority: generousConcordWire(),
	})
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishFn = func(_ context.Context, event nostr.Event) RelayPublishResult {
		return RelayPublishResult{RelayURL: endpoint.url, Accepted: true}
	}
	client, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayAdmission(admission))
	require.NoError(t, err)
	wrap := concordAdmissionEvent(t, 1059)
	ctx := context.Background()
	for range 2 {
		require.NoError(t, publishConcordInvite(ctx, client, []string{endpoint.url}, wrap))
	}
	endpoint.mu.Lock()
	frames := endpoint.publishCalls
	published := len(endpoint.published)
	endpoint.mu.Unlock()
	require.Equal(t, 1, frames, "replayed wrap reached the relay %d times", frames)
	require.Equal(t, 1, published)
}

func TestConcordRotationPublicationBound(t *testing.T) {
	cases := []struct {
		name                              string
		scopes, recipients, invites, want int
	}{
		{name: "empty rotation still admits one", want: 1},
		{name: "one scope one recipient", scopes: 1, recipients: 1, want: 1},
		{name: "chunked scopes and invites", scopes: 2, recipients: 200, invites: 3, want: 2*3 + 3*(1+concordMaxInboxRelays)},
	}
	for _, tc := range cases {
		if got := concordRotationPublicationBound(tc.scopes, tc.recipients, tc.invites); got != tc.want {
			t.Errorf("%s: bound = %d, want %d", tc.name, got, tc.want)
		}
	}
}
