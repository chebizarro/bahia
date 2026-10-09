package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHiveCIPolicyHydratorRecoversFromTransientPublishFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ready atomic.Bool
	called := make(chan int, 2)
	activated := make(chan bool, 1)
	retry := make(chan time.Time, 1)
	attempts := 0
	h := newHiveCIPolicyHydrator(&ready, func(context.Context) error {
		attempts++
		called <- attempts
		if attempts == 1 {
			return errors.New("local publisher unavailable")
		}
		return nil
	}, func(_ context.Context, resume bool) {
		ready.Store(true)
		activated <- resume
	}, zap.NewNop())
	h.retryAfter = func(delay time.Duration) <-chan time.Time {
		require.Equal(t, time.Second, delay)
		return retry
	}
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	h.Start()
	require.Equal(t, 1, receiveHydratorTestValue(t, called))
	require.False(t, ready.Load())
	retry <- time.Now()
	require.Equal(t, 2, receiveHydratorTestValue(t, called))
	require.True(t, receiveHydratorTestValue(t, activated), "recovery must resume retained results")
	require.True(t, ready.Load())
	cancel()
	require.NoError(t, receiveHydratorTestValue(t, done))
}

func TestHiveCIPolicyHydratorKeepsRetryingAfterBackoffCapWithoutNewEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ready atomic.Bool
	called := make(chan int, 8)
	delays := make(chan time.Duration, 7)
	activated := make(chan bool, 1)
	retry := make(chan time.Time, 1)
	attempts := 0
	h := newHiveCIPolicyHydrator(&ready, func(context.Context) error {
		attempts++
		called <- attempts
		if attempts <= 7 {
			return errors.New("local store temporarily unavailable")
		}
		return nil
	}, func(_ context.Context, resume bool) {
		ready.Store(true)
		activated <- resume
	}, zap.NewNop())
	h.retryAfter = func(delay time.Duration) <-chan time.Time {
		delays <- delay
		return retry
	}
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	h.Start()
	for attempt := 1; attempt <= 7; attempt++ {
		require.Equal(t, attempt, receiveHydratorTestValue(t, called))
		shift := attempt - 1
		if shift > hiveCIPolicyMaxRetryShift {
			shift = hiveCIPolicyMaxRetryShift
		}
		require.Equal(t, time.Second<<shift, receiveHydratorTestValue(t, delays))
		retry <- time.Now()
	}
	require.Equal(t, 8, receiveHydratorTestValue(t, called))
	require.True(t, receiveHydratorTestValue(t, activated))
	require.True(t, ready.Load(), "hydration must recover even without another canonical entity event")
	cancel()
	require.NoError(t, receiveHydratorTestValue(t, done))
}

func TestHiveCIPolicyHydratorRetriesWhenCanonicalEntityArrives(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ready, entityPresent atomic.Bool
	called := make(chan int, 3)
	activated := make(chan bool, 2)
	attempts := 0
	h := newHiveCIPolicyHydrator(&ready, func(context.Context) error {
		attempts++
		called <- attempts
		if !entityPresent.Load() {
			return errors.New("canonical environment not yet present")
		}
		return nil
	}, func(_ context.Context, resume bool) {
		ready.Store(true)
		activated <- resume
	}, zap.NewNop())
	// Keep the backoff clock stopped: the validated local-state arrival
	// must reconcile immediately without waiting for a timer.
	h.retryAfter = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	h.Start()
	require.Equal(t, 1, receiveHydratorTestValue(t, called))
	require.False(t, ready.Load())
	pubkey := nostr.Generate().Public().Hex()
	ev := &nostr.Event{Kind: nostr.Kind(kinds.CASControlState), PubKey: nostr.MustPubKeyFromHex(pubkey), Tags: nostr.Tags{
		{"t", kinds.CPStateTopicEnvironmentRegistry},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
	}}
	entityPresent.Store(true)
	h.ObserveEntity(ev, pubkey)
	require.Equal(t, 2, receiveHydratorTestValue(t, called))
	require.True(t, receiveHydratorTestValue(t, activated))
	require.True(t, ready.Load())
	// A later canonical update reconciles config but does not repeatedly
	// consume the retry budget of unrelated pending Hive-CI results.
	h.ObserveEntity(ev, pubkey)
	require.Equal(t, 3, receiveHydratorTestValue(t, called))
	require.False(t, receiveHydratorTestValue(t, activated))
	cancel()
	require.NoError(t, receiveHydratorTestValue(t, done))
}

func receiveHydratorTestValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Hive-CI hydration signal")
		var zero T
		return zero
	}
}
