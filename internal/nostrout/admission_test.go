package nostrout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manual clock. Timers fire only when the test advances time;
// every NewTimer registration is announced on registered so tests synchronize
// on waiters without sleeping.
type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	timers     []*fakeTimer
	registered chan struct{}
}

type fakeTimer struct {
	at      time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0), registered: make(chan struct{}, 1<<16)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	timer := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.fired = true
		timer.ch <- c.now
	} else {
		c.timers = append(c.timers, timer)
	}
	c.mu.Unlock()
	c.registered <- struct{}{}
	return timer.ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		wasActive := !timer.stopped && !timer.fired
		timer.stopped = true
		return wasActive
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.timers[:0]
	for _, timer := range c.timers {
		if timer.stopped {
			continue
		}
		if !timer.at.After(c.now) {
			timer.fired = true
			timer.ch <- c.now
			continue
		}
		remaining = append(remaining, timer)
	}
	c.timers = remaining
}

// awaitTimer blocks until one more timer has been registered. The five-second
// guard only turns a deadlock into a failure; it never orders events.
func (c *fakeClock) awaitTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.registered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an admission waiter to register a timer")
	}
}

func testConfig() Config {
	return Config{
		Aggregate: PurposeBudget{RatePerMinute: 600, Burst: 100},
		PurposeBudgets: map[Purpose]PurposeBudget{
			PurposePriority: {RatePerMinute: 60, Burst: 2},
			PurposeState:    {RatePerMinute: 60, Burst: 2},
			PurposeGeneral:  {RatePerMinute: 60, Burst: 2},
			PurposeBulk:     {RatePerMinute: 60, Burst: 1},
			PurposeSigner:   {RatePerMinute: 60, Burst: 2},
		},
		RelayWire:         PurposeBudget{RatePerMinute: 600, Burst: 100},
		RelayWirePriority: PurposeBudget{RatePerMinute: 600, Burst: 100},
		BreakerMin:        2 * time.Second,
		BreakerMax:        8 * time.Second,
	}
}

func newTestAdmission(cfg Config) (*Admission, *fakeClock) {
	clk := newFakeClock()
	a := newWithClock(cfg, clk)
	a.jitter = func(time.Duration) time.Duration { return 0 }
	return a, clk
}

var eventSerial int

func signedEvent(t *testing.T, kind nostr.Kind) nostr.Event {
	t.Helper()
	eventSerial++
	ev := nostr.Event{Kind: kind, Content: fmt.Sprintf("event-%d", eventSerial), CreatedAt: nostr.Timestamp(1_700_000_000)}
	require.NoError(t, ev.Sign(nostr.Generate()))
	return ev
}

const relayA = "wss://relay-a.example"
const relayB = "wss://relay-b.example"

func publishOnce(t *testing.T, a *Admission, ev nostr.Event, relays []string, outcome func(string) Result) error {
	t.Helper()
	pub, err := a.Begin(context.Background(), ev, relays)
	if err != nil {
		return err
	}
	defer pub.Close()
	for _, relay := range pub.PendingRelays() {
		if err := pub.BeforeAttempt(context.Background(), relay); err != nil {
			return err
		}
		pub.Observe(outcome(relay))
	}
	return nil
}

func accepted(relay string) Result { return Result{RelayURL: relay, Accepted: true} }

func TestMisconfiguredReconciliationCannotExceedLaneOrAggregateBurst(t *testing.T) {
	cfg := testConfig()
	cfg.Aggregate = PurposeBudget{RatePerMinute: 60, Burst: 3}
	a, _ := newTestAdmission(cfg)

	admitted := 0
	for i := 0; i < 1_100; i++ {
		kind := nostr.Kind(1)
		if i%2 == 1 {
			kind = 30_000
		}
		err := publishOnce(t, a, nostr.Event{Kind: kind}, []string{relayA}, accepted)
		if err == nil {
			admitted++
			continue
		}
		require.ErrorIs(t, err, ErrBudgetExceeded)
	}
	// general burst 2 + state burst 2 would allow 4; the aggregate caps at 3.
	require.Equal(t, 3, admitted)
	metrics := a.Metrics()
	require.Equal(t, uint64(1_100), metrics.Attempted)
	require.Equal(t, uint64(3), metrics.Admitted)
	require.Equal(t, uint64(1_097), metrics.BudgetRejected)
	require.Equal(t, uint64(3), metrics.WireAttempts)
}

func TestBudgetRefillsAtConfiguredRate(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted))
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted))
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted), ErrBudgetExceeded)
	clk.Advance(999 * time.Millisecond)
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted), ErrBudgetExceeded)
	clk.Advance(time.Millisecond)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted))
}

func TestPriorityCapacityIsReservedFromStateAndBulkChurn(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	for i := 0; i < 100; i++ {
		_ = publishOnce(t, a, nostr.Event{Kind: 30_000}, []string{relayA}, accepted)
		_ = publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted)
	}
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.NoError(t, err)
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	require.Equal(t, PurposeBulk, pub.Purpose())
	pub.Close()
	op.Close()

	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted))
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1059}, []string{relayA}, accepted))
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 25910}, []string{relayA}, accepted), ErrBudgetExceeded)
}

func TestPurposeForEventClassifiesLanes(t *testing.T) {
	require.Equal(t, PurposePriority, PurposeForEvent(nostr.Event{Kind: 5}))
	require.Equal(t, PurposePriority, PurposeForEvent(nostr.Event{Kind: 1059}))
	require.Equal(t, PurposePriority, PurposeForEvent(nostr.Event{Kind: 25910}))
	require.Equal(t, PurposeSigner, PurposeForEvent(nostr.Event{Kind: 24133}))
	require.Equal(t, PurposeState, PurposeForEvent(nostr.Event{Kind: 10002}))
	require.Equal(t, PurposeState, PurposeForEvent(nostr.Event{Kind: 30315}))
	require.Equal(t, PurposeGeneral, PurposeForEvent(nostr.Event{Kind: 4903}))
	require.Equal(t, PurposeGeneral, PurposeForEvent(nostr.Event{Kind: 20001}))
}

func TestRateLimitOpensSharedCircuitWithBackoff(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	rateLimited := func(relay string) Result { return Result{RelayURL: relay, Reason: "rate-limited: slow down"} }
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, rateLimited))

	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayB}, accepted), ErrCircuitOpen)
	require.True(t, a.State().CircuitOpen)
	clk.Advance(2 * time.Second)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, rateLimited))
	clk.Advance(3 * time.Second)
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted), ErrCircuitOpen, "second rate limit must double the cooldown")
	clk.Advance(time.Second)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted))
	require.True(t, a.Metrics().BreakerUntil.IsZero(), "success after cooldown resets backoff")
	require.Equal(t, uint64(2), a.Metrics().RelayRateLimited)
}

func TestLateSuccessCannotCloseNewerCircuit(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	old, err := a.Begin(context.Background(), signedEvent(t, 1), []string{relayA})
	require.NoError(t, err)
	require.NoError(t, old.BeforeAttempt(context.Background(), relayA))

	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayB}, func(relay string) Result {
		return Result{RelayURL: relay, Reason: "rate-limited: slow down"}
	}))
	clk.Advance(3 * time.Second)
	old.Observe(accepted(relayA))
	old.Close()
	require.False(t, a.Metrics().BreakerUntil.IsZero(), "a success admitted before the newer rate limit must not reset its backoff")

	// The next rate limit therefore doubles rather than restarting at min.
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayB}, func(relay string) Result {
		return Result{RelayURL: relay, Reason: "rate-limited: slow down"}
	}))
	clk.Advance(3 * time.Second)
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted), ErrCircuitOpen)
}

func TestMixedFanOutRecordsAcceptanceAndOpensCircuit(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	ev := signedEvent(t, 1)
	pub, err := a.Begin(context.Background(), ev, []string{relayA, relayB})
	require.NoError(t, err)
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))
	pub.Observe(accepted(relayA))
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayB))
	pub.Observe(Result{RelayURL: relayB, Reason: "rate-limited: slow down"})
	pub.Close()
	require.True(t, a.State().CircuitOpen)
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayB), ErrOperation, "closed publication cannot send")

	// The accepted destination is remembered even though the other relay
	// rate-limited the same publication.
	a.mu.Lock()
	require.True(t, a.hasReceiptLocked(a.clock.Now(), receiptKey{eventID: ev.ID.Hex(), relay: relayA}))
	require.False(t, a.hasReceiptLocked(a.clock.Now(), receiptKey{eventID: ev.ID.Hex(), relay: relayB}))
	a.mu.Unlock()
}

func TestDuplicateSuppressionIsDestinationAware(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	ev := signedEvent(t, 1)
	require.NoError(t, publishOnce(t, a, ev, []string{relayA}, accepted))

	pub, err := a.Begin(context.Background(), ev, []string{relayA, relayB})
	require.NoError(t, err)
	require.Equal(t, []string{relayB}, pub.PendingRelays(), "a newly added relay still receives the event")
	cached := pub.CachedResults()
	require.Len(t, cached, 1)
	require.Equal(t, relayA, cached[0].RelayURL)
	require.False(t, cached[0].Accepted)
	require.True(t, cached[0].Cached)
	require.True(t, cached[0].Succeeded())
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayB))
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayA), ErrOperation, "cached destination must not be sent")
	pub.Observe(accepted(relayB))
	pub.Close()

	before := a.Metrics()
	replay, err := a.Begin(context.Background(), ev, []string{relayA, relayB + "/"})
	require.NoError(t, err)
	require.Empty(t, replay.PendingRelays())
	require.Len(t, replay.CachedResults(), 2)
	replay.Close()
	after := a.Metrics()
	require.Equal(t, before.Admitted, after.Admitted, "a fully cached replay consumes no token")
	require.Equal(t, before.Duplicates+1, after.Duplicates)
}

func TestFailedDestinationRemainsEligibleForReplay(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	ev := signedEvent(t, 1)
	require.NoError(t, publishOnce(t, a, ev, []string{relayA}, func(relay string) Result {
		return Result{RelayURL: relay, Error: errors.New("connection reset")}
	}))
	pub, err := a.Begin(context.Background(), ev, []string{relayA})
	require.NoError(t, err)
	require.Equal(t, []string{relayA}, pub.PendingRelays())
	pub.Close()
}

func TestConcurrentDuplicateIsRejectedButOtherDestinationIsNot(t *testing.T) {
	cfg := testConfig()
	cfg.PurposeBudgets[PurposeGeneral] = PurposeBudget{RatePerMinute: 60, Burst: 5}
	a, _ := newTestAdmission(cfg)
	ev := signedEvent(t, 1)
	first, err := a.Begin(context.Background(), ev, []string{relayA})
	require.NoError(t, err)
	_, err = a.Begin(context.Background(), ev, []string{relayA})
	require.ErrorIs(t, err, ErrInFlight)
	other, err := a.Begin(context.Background(), ev, []string{relayB})
	require.NoError(t, err)
	other.Close()
	first.Close()
	again, err := a.Begin(context.Background(), ev, []string{relayA})
	require.NoError(t, err)
	again.Close()
	require.Equal(t, uint64(1), a.Metrics().InFlightRejected)
}

func TestActivePublicationsAreBounded(t *testing.T) {
	cfg := testConfig()
	cfg.MaxActivePublications = 2
	a, _ := newTestAdmission(cfg)
	one, err := a.Begin(context.Background(), nostr.Event{Kind: 1}, []string{relayA})
	require.NoError(t, err)
	two, err := a.Begin(context.Background(), nostr.Event{Kind: 5}, []string{relayA})
	require.NoError(t, err)
	_, err = a.Begin(context.Background(), nostr.Event{Kind: 30_000}, []string{relayA})
	require.ErrorIs(t, err, ErrCapacity)
	one.Close()
	one.Close()
	two.Close()
	require.Zero(t, a.Metrics().ActivePublications)
}

func TestReceiptCacheIsBounded(t *testing.T) {
	cfg := testConfig()
	cfg.DuplicateLimit = 3
	cfg.PurposeBudgets[PurposeGeneral] = PurposeBudget{RatePerMinute: 600, Burst: 100}
	a, clk := newTestAdmission(cfg)
	events := make([]nostr.Event, 5)
	for i := range events {
		events[i] = signedEvent(t, 1)
		require.NoError(t, publishOnce(t, a, events[i], []string{relayA}, accepted))
	}
	// Refreshing an existing receipt must not grow the order list.
	require.NoError(t, publishOnce(t, a, events[4], []string{relayB}, accepted))
	a.mu.Lock()
	require.LessOrEqual(t, a.receiptOrder.Len(), 3)
	require.Equal(t, a.receiptOrder.Len(), len(a.receipts))
	a.mu.Unlock()

	clk.Advance(11 * time.Minute)
	pub, err := a.Begin(context.Background(), events[4], []string{relayA})
	require.NoError(t, err)
	require.Equal(t, []string{relayA}, pub.PendingRelays(), "receipts expire after the TTL")
	pub.Close()
}

func TestAuthRetrySpendsWireButNotLogicalBudget(t *testing.T) {
	cfg := testConfig()
	cfg.RelayWire = PurposeBudget{RatePerMinute: 60, Burst: 2}
	a, _ := newTestAdmission(cfg)
	pub, err := a.Begin(context.Background(), signedEvent(t, 1), []string{relayA})
	require.NoError(t, err)
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))
	pub.Observe(Result{RelayURL: relayA, Reason: "auth-required: please authenticate"})
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayA), ErrBudgetExceeded)
	pub.Close()
	metrics := a.Metrics()
	require.Equal(t, uint64(1), metrics.Admitted)
	require.Equal(t, uint64(2), metrics.WireAttempts)
	require.Equal(t, uint64(1), metrics.WireRejected)
}

func TestPerRelayWireBudgetIsSharedAcrossPublications(t *testing.T) {
	cfg := testConfig()
	cfg.RelayWire = PurposeBudget{RatePerMinute: 60, Burst: 1}
	a, _ := newTestAdmission(cfg)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted))
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 30_000}, []string{relayA}, accepted), ErrBudgetExceeded)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 30_000}, []string{relayB}, accepted))
}

func TestRelayWireReservesPriorityFrames(t *testing.T) {
	cfg := testConfig()
	cfg.RelayWire = PurposeBudget{RatePerMinute: 60, Burst: 1}
	cfg.RelayWirePriority = PurposeBudget{RatePerMinute: 60, Burst: 1}
	a, _ := newTestAdmission(cfg)
	// Non-priority churn (including AUTH retries) exhausts its wire share...
	pub, err := a.Begin(context.Background(), nostr.Event{Kind: 1}, []string{relayA})
	require.NoError(t, err)
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayA), ErrBudgetExceeded)
	pub.Close()
	// ...but a tombstone still reaches the relay on the reserved share.
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted))
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 1059}, []string{relayA}, accepted), ErrBudgetExceeded)
}

func TestClosedPublicationCannotSendAfterWaiting(t *testing.T) {
	cfg := testConfig()
	cfg.RelayWire = PurposeBudget{RatePerMinute: 60, Burst: 1}
	a, clk := newTestAdmission(cfg)
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.NoError(t, err)
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))

	done := make(chan error, 1)
	go func() { done <- pub.BeforeAttempt(context.Background(), relayA) }()
	clk.awaitTimer(t)
	op.Close()
	clk.Advance(time.Minute)
	require.ErrorIs(t, <-done, ErrOperation, "a waiter must re-validate closure before spending a token")
	require.Equal(t, uint64(1), a.Metrics().WireAttempts)
	pub.Close()
}

func TestExpiredOperationCannotConsumeAvailableCapacity(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.NoError(t, err)
	defer op.Close()
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	clk.Advance(2 * time.Minute)
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayA), ErrQueueTimeout)
	require.Zero(t, a.Metrics().WireAttempts)
	pub.Close()
}

func TestRelayIdentityRegistryIsBoundedWithoutRestoringBurst(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRelayIdentities = 1
	cfg.RelayWire = PurposeBudget{RatePerMinute: 60, Burst: 1}
	a, clk := newTestAdmission(cfg)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, accepted))
	_, err := a.Begin(context.Background(), nostr.Event{Kind: 1}, []string{relayB})
	require.ErrorIs(t, err, ErrCapacity, "a depleted identity cannot be evicted")
	clk.Advance(time.Second)
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayB}, accepted))
}

func TestNormalizeRelayURLPreservesPaths(t *testing.T) {
	require.Equal(t, "wss://relay.example", NormalizeRelayURL(" WSS://Relay.Example/ "))
	require.Equal(t, "wss://relay.example/inbox/", NormalizeRelayURL("wss://relay.example/inbox/"))
	require.Equal(t, "wss://relay.example/?x=1", NormalizeRelayURL("wss://relay.example/?x=1"))
}

func TestKillSwitchHotReloadAndFailClosed(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	dir := t.TempDir()
	path := filepath.Join(dir, "nostr.stop")
	a.killSwitchFile = path

	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted))
	require.NoError(t, os.WriteFile(path, []byte("stop\n"), 0o600))
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted), ErrKillSwitch)
	require.True(t, a.State().KillSwitchActive)
	require.NoError(t, os.WriteFile(path, []byte("resume\n"), 0o600))
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted))

	a.killSwitchFile = dir // unreadable as a file
	require.ErrorIs(t, publishOnce(t, a, nostr.Event{Kind: 5}, []string{relayA}, accepted), ErrKillSwitch)
	state := a.State()
	require.True(t, state.KillSwitchActive)
	require.NotEmpty(t, state.KillSwitchError)
}

func TestKillSwitchBetweenAdmissionAndSendPreventsFrame(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	path := filepath.Join(t.TempDir(), "nostr.stop")
	a.killSwitchFile = path
	pub, err := a.Begin(context.Background(), nostr.Event{Kind: 1}, []string{relayA})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("1"), 0o600))
	require.ErrorIs(t, pub.BeforeAttempt(context.Background(), relayA), ErrKillSwitch)
	pub.Close()
	require.Zero(t, a.Metrics().WireAttempts)
}

func TestNilAdmissionFailsClosed(t *testing.T) {
	var a *Admission
	_, err := a.Begin(context.Background(), nostr.Event{}, []string{relayA})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = a.BeginWaiting(context.Background(), nostr.Event{}, []string{relayA})
	require.ErrorIs(t, err, ErrNotConfigured)
	require.True(t, a.State().KillSwitchActive)
	require.Same(t, Default(), Or(nil))
	require.Same(t, Default(), Default())
}

func TestEmptyDestinationsAreRejectedWithoutConsumingCapacity(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	_, err := a.Begin(context.Background(), nostr.Event{Kind: 1}, []string{" "})
	require.ErrorIs(t, err, ErrNoDestinations)
	require.Zero(t, a.Metrics().Admitted)
}

func TestLargeOperationIsPacedNotFlushedOrRejected(t *testing.T) {
	a, clk := newTestAdmission(testConfig()) // bulk lane: 1/second, burst 1
	const events = 1_100
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: events})
	require.NoError(t, err)
	defer op.Close()

	sent := make(chan int, events)
	errs := make(chan error, 1)
	go func() {
		for i := 0; i < events; i++ {
			pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
			if err != nil {
				errs <- err
				return
			}
			if err := pub.BeforeAttempt(context.Background(), relayA); err != nil {
				errs <- err
				return
			}
			pub.Observe(accepted(relayA))
			pub.Close()
			sent <- i
		}
		close(sent)
	}()

	require.Equal(t, 0, <-sent, "burst admits the first event immediately")
	for i := 1; i < events; i++ {
		clk.awaitTimer(t)
		select {
		case index := <-sent:
			t.Fatalf("event %d was sent before bulk capacity refilled", index)
		case err := <-errs:
			t.Fatalf("operation failed mid-way: %v", err)
		default:
		}
		clk.Advance(time.Second)
		require.Equal(t, i, <-sent)
	}
	_, open := <-sent
	require.False(t, open)
	require.Zero(t, op.Remaining())
	_, err = op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.ErrorIs(t, err, ErrOperation, "an operation cannot exceed its declared size")
}

func TestOperationWaitsForCircuitInsteadOfFailing(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 2})
	require.NoError(t, err)
	defer op.Close()
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	require.NoError(t, pub.BeforeAttempt(context.Background(), relayA))
	pub.Observe(Result{RelayURL: relayA, Reason: "rate-limited: slow down"})
	pub.Close()

	done := make(chan error, 1)
	go func() {
		next, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
		if err == nil {
			next.Close()
		}
		done <- err
	}()
	clk.awaitTimer(t)
	select {
	case err := <-done:
		t.Fatalf("operation publication must wait for the open circuit: %v", err)
	default:
	}
	clk.Advance(2 * time.Second)
	require.NoError(t, <-done)
}

func TestKillSwitchAbortsWaitingOperation(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	path := filepath.Join(t.TempDir(), "nostr.stop")
	a.killSwitchFile = path
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 3})
	require.NoError(t, err)
	defer op.Close()
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	pub.Close()

	done := make(chan error, 1)
	go func() {
		_, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
		done <- err
	}()
	clk.awaitTimer(t)
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	clk.Advance(time.Second)
	require.ErrorIs(t, <-done, ErrKillSwitch)
}

func TestOperationRejectsInvalidDeclarationsAndOverlappingPublications(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	for _, size := range []int{0, -1, 2_049} {
		_, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: size})
		require.ErrorIs(t, err, ErrOperation, "size %d", size)
	}
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 2})
	require.NoError(t, err)
	pub, err := op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.NoError(t, err)
	_, err = op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.ErrorIs(t, err, ErrOperation)
	pub.Close()
	op.Close()
	op.Close()
	_, err = op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.ErrorIs(t, err, ErrOperation)
}

func TestOperationDeadlineBoundsLifetime(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	op, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 3})
	require.NoError(t, err)
	defer op.Close()
	require.Equal(t, clk.Now().Add(3*time.Second+time.Minute), op.Deadline())
	clk.Advance(3*time.Second + time.Minute)
	_, err = op.Begin(context.Background(), signedEvent(t, 1059), []string{relayA})
	require.ErrorIs(t, err, ErrOperation)
	require.ErrorIs(t, err, ErrQueueTimeout)
}

func TestOperationQueueIsBoundedFIFOAndCancellable(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	active, err := a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.NoError(t, err)

	type started struct {
		index int
		op    *Operation
		err   error
	}
	results := make(chan started, 8)
	cancels := make([]context.CancelFunc, 8)
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		go func(index int) {
			op, err := a.BeginOperation(ctx, OperationSpec{MaxEvents: 1})
			results <- started{index: index, op: op, err: err}
		}(i)
		clk.awaitTimer(t)
	}
	_, err = a.BeginOperation(context.Background(), OperationSpec{MaxEvents: 1})
	require.ErrorIs(t, err, ErrQueueFull)
	require.Equal(t, 8, a.Metrics().OperationsQueued)

	cancels[1]()
	cancelled := <-results
	require.Equal(t, 1, cancelled.index)
	require.ErrorIs(t, cancelled.err, context.Canceled)
	require.Equal(t, 7, a.Metrics().OperationsQueued)

	active.Close()
	next := <-results
	require.Equal(t, 0, next.index, "activation is FIFO")
	require.NoError(t, next.err)
	next.op.Close()
	third := <-results
	require.Equal(t, 2, third.index, "a cancelled waiter is skipped")
	require.NoError(t, third.err)

	clk.Advance(30 * time.Second)
	for i := 0; i < 5; i++ {
		timedOut := <-results
		require.ErrorIs(t, timedOut.err, ErrQueueTimeout)
	}
	third.op.Close()
	require.False(t, a.Metrics().OperationActive)
	for _, cancel := range cancels {
		cancel()
	}
}

func TestBeginWaitingIsBoundedAndPaced(t *testing.T) {
	cfg := testConfig()
	cfg.MaxWaiters = 1
	a, clk := newTestAdmission(cfg)
	ctx := context.Background()
	relays := []string{relayA, relayB}
	for i := 0; i < 2; i++ { // signer lane burst
		pub, err := a.BeginWaiting(ctx, nostr.Event{Kind: 24133}, relays)
		require.NoError(t, err)
		require.Equal(t, PurposeSigner, pub.Purpose())
		pub.Close()
	}

	done := make(chan error, 1)
	begin := func() {
		pub, err := a.BeginWaiting(ctx, nostr.Event{Kind: 24133}, relays)
		if err == nil {
			pub.Close()
		}
		done <- err
	}
	go begin()
	clk.awaitTimer(t)
	_, err := a.BeginWaiting(ctx, nostr.Event{Kind: 24133}, relays)
	require.ErrorIs(t, err, ErrQueueFull, "concurrent waiters are bounded")
	clk.Advance(time.Second)
	require.NoError(t, <-done, "capacity refilled during the bounded wait")

	go begin()
	clk.awaitTimer(t)
	clk.Advance(30 * time.Second)
	require.ErrorIs(t, <-done, ErrQueueTimeout, "a waiter woken at its deadline must not consume capacity")
}

func TestBeginWaitingPacesAnOpenCircuitButNotTheKillSwitch(t *testing.T) {
	a, clk := newTestAdmission(testConfig())
	path := filepath.Join(t.TempDir(), "stop")
	a.killSwitchFile = path
	require.NoError(t, publishOnce(t, a, nostr.Event{Kind: 1}, []string{relayA}, func(relay string) Result {
		return Result{RelayURL: relay, Reason: "rate-limited: slow down"}
	}))
	done := make(chan error, 1)
	go func() {
		pub, err := a.BeginWaiting(context.Background(), nostr.Event{Kind: 24133}, []string{relayA})
		if err == nil {
			pub.Close()
		}
		done <- err
	}()
	clk.awaitTimer(t)
	clk.Advance(2 * time.Second)
	require.NoError(t, <-done, "a signer request waits out the breaker instead of failing")

	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	_, err := a.BeginWaiting(context.Background(), nostr.Event{Kind: 24133}, []string{relayA})
	require.ErrorIs(t, err, ErrKillSwitch)
}

func TestConcurrentPublishersCannotExceedSharedBurst(t *testing.T) {
	cfg := testConfig()
	cfg.Aggregate = PurposeBudget{RatePerMinute: 60, Burst: 5}
	a, _ := newTestAdmission(cfg)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	kinds := []nostr.Kind{1, 5, 1059, 30_000, 25910, 4903}
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(kind nostr.Kind) {
			defer wg.Done()
			<-start
			if err := publishOnce(t, a, nostr.Event{Kind: kind}, []string{relayA, relayB}, accepted); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(kinds[i%len(kinds)])
	}
	close(start)
	wg.Wait()
	require.Equal(t, 5, admitted)
	require.Equal(t, uint64(10), a.Metrics().WireAttempts)
	require.Zero(t, a.Metrics().ActivePublications)
}
