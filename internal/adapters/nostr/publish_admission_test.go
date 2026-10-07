package nostr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// generousAdmissionLanes returns per-lane budgets that never reject inside a
// test, so a case can tighten exactly the budget it asserts on.
func generousAdmissionLanes() map[nostrout.Purpose]nostrout.PurposeBudget {
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	return map[nostrout.Purpose]nostrout.PurposeBudget{
		nostrout.PurposePriority: generous,
		nostrout.PurposeState:    generous,
		nostrout.PurposeGeneral:  generous,
		nostrout.PurposeBulk:     generous,
		nostrout.PurposeSigner:   generous,
	}
}

// newAdmissionTestPublisher wires a Publisher whose pool enforces admission
// for real: EVENT frames are counted at the publish seam, deliveries run on
// the local outbox, and retries are driven by the tests instead of by sleep.
func newAdmissionTestPublisher(t *testing.T, admission *nostrout.Admission, repo *signalingOutbox, urls ...string) (*Publisher, *atomic.Int32) {
	t.Helper()
	var frames atomic.Int32
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		frames.Add(1)
		return nil
	})
	pool := connectedTestPool(t, admission, urls...)
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true},
		pool,
		repo,
		zap.NewNop(),
		WithLocalOutbox(openDeliveryTestOutbox(t), nil),
	)
	publisher.newBackoff = func() *Backoff {
		return &Backoff{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1}
	}
	publisher.idleInterval = time.Hour
	return publisher, &frames
}

// TestPublisherAdmissionRefusalIsBackPressureNotAbandonment pins the contract
// with the outbox (docs/architecture/outbox-delivery.md): a round the shared
// controller refuses before relay I/O never counts against the attempt budget,
// never abandons the entry, and never marks its coordinate undelivered; when
// the gate lifts, the same entry delivers normally.
func TestPublisherAdmissionRefusalIsBackPressureNotAbandonment(t *testing.T) {
	ctx := context.Background()
	killPath := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(killPath, []byte("stop"), 0o600))
	admission := nostrout.New(nostrout.Config{
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		KillSwitchFile:    killPath,
	})
	repo := newSignalingOutbox()
	publisher, frames := newAdmissionTestPublisher(t, admission, repo, relayA)

	event := testSignedEvent("admission-backpressure")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishIncomplete, "a refused first round leaves the event queued, not failed")
	require.Contains(t, err.Error(), "kill switch active")
	require.Zero(t, frames.Load(), "kill switch must prevent every EVENT frame")

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Zero(t, entry.Rounds)

	// Ten more refused rounds: the attempt budget does not move, nothing is
	// abandoned, and no relay is contacted.
	d, _ := publisher.trackDelivery(*event, entry.Rounds)
	for range 10 {
		d.mu.Lock()
		report := publisher.deliverRound(ctx, d)
		d.mu.Unlock()
		require.True(t, report.admissionRefused)
		require.True(t, report.admissionHalt, "the kill switch is a process-wide gate")
		require.False(t, report.settled)
		require.Zero(t, d.rounds)
	}
	require.Zero(t, frames.Load())
	entry, found, err = publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State, "refused rounds never abandon")
	require.Zero(t, entry.Rounds, "refused rounds never count against the attempt budget")

	// Lifting the kill switch delivers the same entry on the next round.
	require.NoError(t, os.WriteFile(killPath, []byte("resume"), 0o600))
	d.mu.Lock()
	report := publisher.deliverRound(ctx, d)
	d.mu.Unlock()
	require.True(t, report.delivered)
	require.Equal(t, 1, d.rounds)
	require.Equal(t, int32(1), frames.Load())

	select {
	case id := <-repo.abandoned:
		t.Fatalf("admission back-pressure abandoned %s", id)
	default:
	}
}

// TestPublisherRedeliveryPassHaltsOnProcessWideGate proves a kill switch stops
// the runner's whole pass at the first refusal, while a lane budget refusal
// only skips the entries it applies to.
func TestPublisherRedeliveryPassHaltsOnProcessWideGate(t *testing.T) {
	ctx := context.Background()
	killPath := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(killPath, []byte("stop"), 0o600))
	admission := nostrout.New(nostrout.Config{
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		KillSwitchFile:    killPath,
	})
	repo := newSignalingOutbox()
	publisher, frames := newAdmissionTestPublisher(t, admission, repo, relayA)

	events := make([]gonostr.Event, 0, 3)
	for i := range 3 {
		ev := testSignedEvent(fmt.Sprintf("halt-%d", i))
		require.NoError(t, ev.Sign(gonostr.Generate()))
		require.NoError(t, publisher.Enqueue(ctx, *ev, "halt.test", nil))
		d, _ := publisher.trackDelivery(*ev, 0)
		publisher.scheduleDelivery(d, publisher.now().Add(-time.Second))
		events = append(events, *ev)
	}

	publisher.redeliverDue(ctx)
	require.Equal(t, uint64(1), admission.Metrics().Attempted, "the pass must stop at the first kill-switch refusal")
	require.Zero(t, frames.Load())

	// A budget refusal is not a halt: every due entry is offered its round,
	// because other entries may sit in a lane that still has capacity. One
	// event fits the single aggregate token; the other two are refused and
	// stay pending.
	require.NoError(t, os.WriteFile(killPath, []byte("resume"), 0o600))
	tight := nostrout.New(nostrout.Config{
		Aggregate: nostrout.PurposeBudget{RatePerMinute: 1, Burst: 1},
	})
	publisher.pool.outboundAdmission = tight
	publisher.deliveriesMu.Lock()
	for _, d := range publisher.deliveries {
		d.nextAt = publisher.now().Add(-time.Second)
	}
	publisher.deliveriesMu.Unlock()

	publisher.redeliverDue(ctx)
	require.Equal(t, uint64(3), tight.Metrics().Attempted, "a budget refusal must not halt the pass")
	require.Equal(t, int32(1), frames.Load(), "the single admitted token delivers exactly one frame")
}

// TestPublisherRestartHydrationCannotBurst proves a restart's discovery pass
// over a large pending outbox stays inside the aggregate burst: entries past
// the budget are skipped and remain pending instead of flooding the relay.
func TestPublisherRestartHydrationCannotBurst(t *testing.T) {
	ctx := context.Background()
	admission := nostrout.New(nostrout.Config{
		Aggregate:         nostrout.PurposeBudget{RatePerMinute: 6, Burst: 5},
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
	})
	repo := newSignalingOutbox()
	publisher, frames := newAdmissionTestPublisher(t, admission, repo, relayA)

	const pending = 50
	for i := range pending {
		ev := testSignedEvent(fmt.Sprintf("hydrate-%d", i))
		require.NoError(t, ev.Sign(gonostr.Generate()))
		_, err := publisher.localOutbox.Enqueue(localstore.OutboxEntry{
			Event:      *ev,
			EntityType: "hydration.test",
			EnqueuedAt: publisher.now(),
		})
		require.NoError(t, err)
	}

	more, err := publisher.discoverPending(ctx)
	require.NoError(t, err)
	require.False(t, more)
	require.Equal(t, int32(5), frames.Load(), "hydration must stop at the aggregate burst")

	listed, err := publisher.localOutbox.ListPending("", nil, pending+10)
	require.NoError(t, err)
	require.Len(t, listed, pending-5, "refused entries stay pending for a later pass")
	select {
	case id := <-repo.abandoned:
		t.Fatalf("hydration abandoned %s", id)
	default:
	}
}

// TestPublisherWireRefusalRoundsDoNotConsumeAttemptBudget: once a relay's
// wire budget is spent, later rounds are refused per frame by the controller
// and reach no relay; like a whole-call refusal they never count against the
// attempt budget, and the entry delivers normally once the pressure lifts.
func TestPublisherWireRefusalRoundsDoNotConsumeAttemptBudget(t *testing.T) {
	ctx := context.Background()
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	admission := nostrout.New(nostrout.Config{
		Aggregate:         generous,
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 60, Burst: 1},
		RelayWirePriority: generous,
	})
	repo := newSignalingOutbox()
	publisher, frames := newAdmissionTestPublisher(t, admission, repo, relayA)
	publisher.maxAttempts = 2

	first := testSignedEvent("wire-first")
	_, err := publisher.PublishSignedEventWithResults(ctx, first)
	require.NoError(t, err, "the first frame fits the single wire token")
	require.Equal(t, int32(1), frames.Load())
	require.Equal(t, first.ID.Hex(), receive(t, repo.published, "first event published"))

	second := testSignedEvent("wire-second")
	_, err = publisher.PublishSignedEventWithResults(ctx, second)
	require.ErrorIs(t, err, ErrPublishIncomplete, "the wire-refused event stays queued")
	require.Equal(t, int32(1), frames.Load(), "no frame may reach the relay while its wire budget is spent")

	d, _ := publisher.trackDelivery(*second, 0)
	for range 3 {
		d.mu.Lock()
		report := publisher.deliverRound(ctx, d)
		d.mu.Unlock()
		require.True(t, report.admissionRefused)
		require.False(t, report.admissionHalt, "a per-relay wire budget is not a process-wide gate")
		require.False(t, report.settled)
		require.Zero(t, d.rounds)
	}
	require.Equal(t, int32(1), frames.Load())
	entry, found, err := publisher.localOutbox.Get(second.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Zero(t, entry.Rounds, "wire refusals never count against the attempt budget")

	// Pressure lifts: the same entry delivers on the next round.
	publisher.pool.outboundAdmission = nostrout.New(nostrout.Config{
		Aggregate:         generous,
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         generous,
		RelayWirePriority: generous,
	})
	d.mu.Lock()
	report := publisher.deliverRound(ctx, d)
	d.mu.Unlock()
	require.True(t, report.delivered)
	require.Equal(t, 1, d.rounds)
	require.Equal(t, int32(2), frames.Load())
	require.Equal(t, second.ID.Hex(), receive(t, repo.published, "entry published once the wire budget lifted"))
}
