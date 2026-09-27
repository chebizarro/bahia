package nostr

import (
	"errors"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func newTestOutboundAdmission(now *time.Time, rate, burst int) *OutboundAdmission {
	admission := NewOutboundAdmission(OutboundAdmissionConfig{
		RatePerMinute: rate,
		Burst:         burst,
		BreakerMin:    2 * time.Second,
		BreakerMax:    8 * time.Second,
	})
	admission.lastRefill = *now
	admission.now = func() time.Time { return *now }
	return admission
}

func TestOutboundAdmissionBoundsMisconfiguredSnapshotLoop(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 30, 10)

	for i := 0; i < 1_100; i++ {
		err := admission.admit(gonostr.Event{Kind: 31990})
		if i < 10 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrOutboundBudgetExceeded)
		}
	}

	metrics := admission.Metrics()
	require.Equal(t, uint64(1_100), metrics.Attempted)
	require.Equal(t, uint64(10), metrics.Admitted)
	require.Equal(t, uint64(1_090), metrics.BudgetRejected)
}

func TestOutboundAdmissionRefillsAtConfiguredRate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 30, 1)
	require.NoError(t, admission.admit(gonostr.Event{}))
	require.ErrorIs(t, admission.admit(gonostr.Event{}), ErrOutboundBudgetExceeded)

	now = now.Add(2 * time.Second)
	require.NoError(t, admission.admit(gonostr.Event{}))
}

func TestOutboundAdmissionRateLimitOpensSharedCircuit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 60, 10)
	require.NoError(t, admission.admit(gonostr.Event{}))

	admission.observe([]PublishResult{{RelayURL: "wss://relay.example", Reason: "rate-limited: slow down"}})
	err := admission.admit(gonostr.Event{})
	require.ErrorIs(t, err, ErrOutboundCircuitOpen)
	require.Equal(t, uint64(1), admission.Metrics().RelayRateLimited)

	now = now.Add(2 * time.Second)
	require.NoError(t, admission.admit(gonostr.Event{}))
}

func TestOutboundAdmissionSuccessfulPublishResetsBreaker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 60, 10)
	admission.observe([]PublishResult{{RelayURL: "wss://relay.example", Reason: "rate-limited: slow down"}})
	now = now.Add(2 * time.Second)
	require.NoError(t, admission.admit(gonostr.Event{}))
	admission.observe([]PublishResult{{RelayURL: "wss://relay.example", Accepted: true}})
	require.True(t, admission.Metrics().BreakerUntil.IsZero())
}

func TestWithOutboundAdmissionSharesBudgetAcrossPools(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 60, 1)
	one := NewRelayPool(nil, nil, WithOutboundAdmission(admission))
	two := NewRelayPool(nil, nil, WithOutboundAdmission(admission))

	_, err := one.Publish(t.Context(), gonostr.Event{})
	require.NoError(t, err)
	_, err = two.Publish(t.Context(), gonostr.Event{})
	require.True(t, errors.Is(err, ErrOutboundBudgetExceeded), "second pool must share first pool's exhausted budget: %v", err)
}
