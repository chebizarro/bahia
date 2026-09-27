package nostr

import (
	"errors"
	"os"
	"path/filepath"
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
	for _, bucket := range admission.buckets {
		bucket.lastRefill = *now
	}
	admission.now = func() time.Time { return *now }
	return admission
}

func TestOutboundAdmissionBoundsMisconfiguredSnapshotLoop(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 30, 10)

	for i := 0; i < 1_100; i++ {
		err := admission.admit(gonostr.Event{Kind: 1})
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

	admission.observe(gonostr.Event{}, []PublishResult{{RelayURL: "wss://relay.example", Reason: "rate-limited: slow down"}})
	err := admission.admit(gonostr.Event{})
	require.ErrorIs(t, err, ErrOutboundCircuitOpen)
	require.Equal(t, uint64(1), admission.Metrics().RelayRateLimited)

	now = now.Add(2 * time.Second)
	require.NoError(t, admission.admit(gonostr.Event{}))
}

func TestOutboundAdmissionSuccessfulPublishResetsBreaker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 60, 10)
	admission.observe(gonostr.Event{}, []PublishResult{{RelayURL: "wss://relay.example", Reason: "rate-limited: slow down"}})
	now = now.Add(2 * time.Second)
	require.NoError(t, admission.admit(gonostr.Event{}))
	admission.observe(gonostr.Event{}, []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}})
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

func TestOutboundAdmissionKillSwitchHotReload(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	path := filepath.Join(t.TempDir(), "nostr.stop")
	admission := newTestOutboundAdmission(&now, 60, 10)
	admission.killSwitchFile = path

	require.NoError(t, admission.admit(gonostr.Event{Content: "before"}))
	require.NoError(t, os.WriteFile(path, []byte("stop\n"), 0o600))
	require.ErrorIs(t, admission.admit(gonostr.Event{Content: "blocked"}), ErrOutboundKillSwitch)
	require.True(t, admission.State().KillSwitchActive)
	require.NoError(t, os.WriteFile(path, []byte("resume\n"), 0o600))
	require.NoError(t, admission.admit(gonostr.Event{Content: "after"}))
	require.False(t, admission.State().KillSwitchActive)
	require.Equal(t, uint64(1), admission.Metrics().KillSwitchRejected)
}

func TestOutboundAdmissionSuppressesAcceptedDuplicate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := newTestOutboundAdmission(&now, 60, 10)
	ev := gonostr.Event{Kind: 1, Content: "same", CreatedAt: gonostr.Timestamp(now.Unix())}
	ev.ID = ev.GetID()
	require.NoError(t, admission.admit(ev))
	admission.observe(ev, []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}})
	require.ErrorIs(t, admission.admit(ev), ErrOutboundDuplicate)
	require.Equal(t, uint64(1), admission.Metrics().Duplicates)
}

func TestOutboundAdmissionReservesPriorityCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	admission := NewOutboundAdmission(OutboundAdmissionConfig{
		BreakerMin: 2 * time.Second,
		BreakerMax: 8 * time.Second,
		PurposeBudgets: map[OutboundPurpose]OutboundPurposeBudget{
			OutboundPurposePriority: {RatePerMinute: 60, Burst: 1},
			OutboundPurposeState:    {RatePerMinute: 60, Burst: 1},
			OutboundPurposeGeneral:  {RatePerMinute: 60, Burst: 1},
		},
	})
	for _, bucket := range admission.buckets {
		bucket.lastRefill = now
	}
	admission.now = func() time.Time { return now }

	require.NoError(t, admission.admit(gonostr.Event{Kind: 1, Content: "general"}))
	require.ErrorIs(t, admission.admit(gonostr.Event{Kind: 1, Content: "general-2"}), ErrOutboundBudgetExceeded)
	require.NoError(t, admission.admit(gonostr.Event{Kind: 5, Content: "tombstone"}))
	require.NoError(t, admission.admit(gonostr.Event{Kind: 30_001, Content: "state"}))
}
