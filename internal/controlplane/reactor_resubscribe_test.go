package controlplane

import (
	"errors"
	"testing"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/stretchr/testify/require"
)

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
