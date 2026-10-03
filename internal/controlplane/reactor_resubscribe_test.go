package controlplane

import (
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestReactorReconnectRetainsSinceUntilAnEventAdvancesCursor(t *testing.T) {
	r := NewReactor(Config{}, nil, nostrpool.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())
	initial := r.buildRequestSubscriptionFiltersForCurrentCursor(t.Context())
	require.Len(t, initial, 1)
	require.NotZero(t, initial[0].Since)

	resumed := r.buildRequestSubscriptionFiltersForReconnect(initial)
	require.Equal(t, initial[0].Since, resumed[0].Since,
		"an empty generation must not skip events published while disconnected")

	createdAt := nostr.Now()
	r.handleEvent(t.Context(), signedControlPlaneTestEventAt(t, nostrpool.KindCASControlState, createdAt))
	resumed = r.buildRequestSubscriptionFiltersForReconnect(initial)
	require.Equal(t, replayCursorWithOverlap(createdAt), resumed[0].Since,
		"a delivered event advances the reconnect filter before its REQ")
}

func TestKeepSubscriptionOnResubscribeFailurePreservesCurrent(t *testing.T) {
	current := &nostrpool.MergedSubscription{}
	got, err := keepSubscriptionOnResubscribeFailure(current, func() (*nostrpool.MergedSubscription, error) {
		return nil, errors.New("relay unavailable")
	})
	require.EqualError(t, err, "relay unavailable")
	require.Same(t, current, got)
}

func TestKeepSubscriptionOnResubscribeFailureRejectsNilSuccess(t *testing.T) {
	current := &nostrpool.MergedSubscription{}
	got, err := keepSubscriptionOnResubscribeFailure(current, func() (*nostrpool.MergedSubscription, error) {
		return nil, nil
	})
	require.EqualError(t, err, "relay resubscribe returned a nil subscription")
	require.Same(t, current, got)
}

func TestKeepSubscriptionOnResubscribeFailureUsesReplacement(t *testing.T) {
	current := &nostrpool.MergedSubscription{}
	replacement := &nostrpool.MergedSubscription{}
	got, err := keepSubscriptionOnResubscribeFailure(current, func() (*nostrpool.MergedSubscription, error) {
		return replacement, nil
	})
	require.NoError(t, err)
	require.Same(t, replacement, got)
}
