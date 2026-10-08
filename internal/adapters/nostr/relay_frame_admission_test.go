package nostr

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip77"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- NIP-42 AUTH frames ---

// TestAuthStormAcrossFlappingConnectionsCannotExceedBudget: a relay that
// challenges every connection (flapping reconnects, per-connection AUTH)
// cannot pull unbounded AUTH frames from the process. Six sequential
// connections share one controller whose reserved priority burst is 2: the
// first two authenticate, every later AUTH attempt fails closed before the
// frame is written, and the relay never sees more AUTH frames than permits.
func TestAuthStormAcrossFlappingConnectionsCannotExceedBudget(t *testing.T) {
	admission := nostrout.New(nostrout.Config{
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: {RatePerMinute: 1, Burst: 2},
		},
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
	})
	var relayAuthFrames atomic.Int32
	srv := newPoolKhatruRelay(t, func(relay *khatru.Relay) {
		requireNIP42(true)(relay)
		relay.OnAuth = func(context.Context, gonostr.PubKey) { relayAuthFrames.Add(1) }
	})
	secret := gonostr.Generate()
	var signCalls atomic.Int32

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authenticated := 0
	for range 6 {
		pool := NewRelayPool([]string{srv.url}, zap.NewNop(),
			WithPrivateKey(secret.Hex()),
			WithOutboundAdmission(admission),
			WithAuthSignFunc(func(_ context.Context, event *gonostr.Event) error {
				signCalls.Add(1)
				return event.Sign(secret)
			}))
		pool.Connect(ctx)
		err := pool.AuthenticateRelays(ctx, nil)
		pool.Close()
		if err == nil {
			authenticated++
			continue
		}
		require.ErrorIs(t, err, nostrout.ErrBudgetExceeded,
			"a refused AUTH must fail closed with the admission cause, got %v", err)
	}
	require.Equal(t, 2, authenticated, "only the reserved priority burst may authenticate")
	require.Equal(t, int32(2), signCalls.Load(), "a refused permit must never reach the signer")
	require.Equal(t, uint64(2), admission.Metrics().AuthAdmitted)
	require.LessOrEqual(t, relayAuthFrames.Load(), int32(2), "the relay saw more AUTH frames than permits")
	require.Greater(t, admission.Metrics().BudgetRejected+admission.Metrics().WireRejected, uint64(0))
}

// TestPublishAuthRetryChargesAuthAndWirePermits drives the real stack: a
// khatru relay that rejects unauthenticated EVENTs with "auth-required:".
// The publish round spends one AUTH permit for the challenge and a second
// per-relay wire token for the retried EVENT frame.
func TestPublishAuthRetryChargesAuthAndWirePermits(t *testing.T) {
	admission := newIsolatedTestAdmission()
	srv := newPoolKhatruRelay(t, requireNIP42(false))
	secret := gonostr.Generate()
	pool := NewRelayPool([]string{srv.url}, zap.NewNop(),
		WithPrivateKey(secret.Hex()),
		WithOutboundAdmission(admission))
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool.Connect(ctx)

	ev := signedStackEvent(t, secret, 1, "auth-retry")
	results, err := pool.PublishWithResults(ctx, ev)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Accepted, "the AUTH retry must deliver the event: %+v", results[0])
	require.Equal(t, uint64(1), admission.Metrics().AuthAdmitted)
	require.Equal(t, uint64(3), admission.Metrics().WireAttempts, "first EVENT frame + AUTH frame + retry EVENT frame")

	// The receipt cache answers a replay locally: no new frame, no new permit.
	results, err = pool.PublishWithResults(ctx, ev)
	require.NoError(t, err)
	require.True(t, results[0].IsDuplicate())
	require.Equal(t, uint64(1), admission.Metrics().AuthAdmitted)
	require.Equal(t, uint64(3), admission.Metrics().WireAttempts)
}

// TestKillSwitchStopsAuthFrames: with the kill switch active, neither AUTH
// nor EVENT frames reach the wire, and the AUTH attempt fails closed with
// the kill-switch cause.
func TestKillSwitchStopsAuthFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	admission := nostrout.New(nostrout.Config{KillSwitchFile: path})
	var relayAuthFrames atomic.Int32
	srv := newPoolKhatruRelay(t, func(relay *khatru.Relay) {
		requireNIP42(true)(relay)
		relay.OnAuth = func(context.Context, gonostr.PubKey) { relayAuthFrames.Add(1) }
	})
	secret := gonostr.Generate()
	var signCalls atomic.Int32
	pool := NewRelayPool([]string{srv.url}, zap.NewNop(),
		WithPrivateKey(secret.Hex()),
		WithOutboundAdmission(admission),
		WithAuthSignFunc(func(_ context.Context, event *gonostr.Event) error {
			signCalls.Add(1)
			return event.Sign(secret)
		}))
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool.Connect(ctx)

	err := pool.AuthenticateRelays(ctx, nil)
	require.ErrorIs(t, err, nostrout.ErrKillSwitch)
	_, pubErr := pool.PublishWithResults(ctx, signedStackEvent(t, secret, 1, "gated"))
	require.ErrorIs(t, pubErr, nostrout.ErrKillSwitch)
	require.Zero(t, signCalls.Load())
	require.Zero(t, relayAuthFrames.Load())
	require.Zero(t, admission.Metrics().AuthAdmitted)
}

// --- NIP-77 negentropy uploads ---

// negentropyFakeSource serves a fixed event set by id, like the local store
// side of a NIP-77 session.
type negentropyFakeSource struct {
	events map[gonostr.ID]gonostr.Event
}

func (s *negentropyFakeSource) QueryEvents(filter gonostr.Filter) iter.Seq[gonostr.Event] {
	return func(yield func(gonostr.Event) bool) {
		for _, id := range filter.IDs {
			if ev, ok := s.events[id]; ok {
				if !yield(ev) {
					return
				}
			}
		}
	}
}

func negentropyUploadSetup(t *testing.T, count int, admission *nostrout.Admission) (*RelayPool, *negentropyFakeSource, *gonostr.Relay, *atomic.Int32, chan gonostr.ID) {
	t.Helper()
	var frames atomic.Int32
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		frames.Add(1)
		return nil
	})
	pool := NewRelayPool(nil, zap.NewNop(), WithOutboundAdmission(admission))
	source := &negentropyFakeSource{events: make(map[gonostr.ID]gonostr.Event, count)}
	ids := make(chan gonostr.ID, count)
	for i := range count {
		ev := gonostr.Event{Kind: 30315, CreatedAt: gonostr.Timestamp(1_700_000_000 + i),
			Tags: gonostr.Tags{{"d", fmt.Sprintf("neg-%d", i)}}, Content: fmt.Sprintf("upload-%d", i)}
		require.NoError(t, ev.Sign(gonostr.Generate()))
		source.events[ev.ID] = ev
		ids <- ev.ID
	}
	close(ids)
	relay := gonostr.NewRelay(context.Background(), "wss://negentropy.example", gonostr.RelayOptions{})
	return pool, source, relay, &frames, ids
}

// TestNegentropyUploadOf1100EventsIsPacedWithinBudgets: a large reconcile
// upload cannot burst. The aggregate budget (burst 25, then 1000/s) paces
// all 1100 events through one declared operation; the run takes at least as
// long as the paced refill requires, every frame is charged, and a replay of
// the same set is suppressed by the receipt cache.
func TestNegentropyUploadOf1100EventsIsPacedWithinBudgets(t *testing.T) {
	const count = 1100
	admission := nostrout.New(nostrout.Config{
		Aggregate:      nostrout.PurposeBudget{RatePerMinute: 60_000, Burst: 25},
		PurposeBudgets: generousAdmissionLanes(),
		RelayWire:      nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000},
	})
	pool, source, relay, frames, ids := negentropyUploadSetup(t, count, admission)

	start := time.Now()
	pool.uploadNegentropyItems(t.Context(), nip77Direction(source, relay, ids), relay)
	elapsed := time.Since(start)

	require.Equal(t, int32(count), frames.Load(), "every local-only event is uploaded")
	require.Equal(t, uint64(1), admission.Metrics().OperationsStarted)
	require.Equal(t, uint64(count), admission.Metrics().Admitted)
	require.Equal(t, uint64(count), admission.Metrics().WireAttempts)
	// 1100 events at 1000/s after a 25-event burst cannot finish faster than
	// (1100-25)/1000s; a flush would finish in milliseconds.
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond, "upload must be paced, not flushed")

	// A resumed session replays nothing: every destination already holds a
	// receipt, so the same 1100 events are answered locally with no frame.
	replay := make(chan gonostr.ID, count)
	for id := range source.events {
		replay <- id
	}
	close(replay)
	pool.uploadNegentropyItems(t.Context(), nip77Direction(source, relay, replay), relay)
	require.Equal(t, int32(count), frames.Load(), "a resumed session must not resend accepted events")
}

// nip77Direction assembles the upload direction of a NIP-77 session: the
// local source answers id queries and the raw session relay is the target.
func nip77Direction(source *negentropyFakeSource, relay *gonostr.Relay, ids chan gonostr.ID) nip77.Direction {
	return nip77.Direction{From: source, To: relay, Items: ids}
}

// TestNegentropyUploadChunksBeyondOneOperation: more events than one
// operation may declare are uploaded as consecutive paced operations, all of
// them admitted, none of them flushed around the controller.
func TestNegentropyUploadChunksBeyondOneOperation(t *testing.T) {
	const count = 2100
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	admission := nostrout.New(nostrout.Config{
		Aggregate:         generous,
		PurposeBudgets:    generousAdmissionLanes(),
		RelayWire:         generous,
		RelayWirePriority: generous,
	})
	pool, source, relay, frames, ids := negentropyUploadSetup(t, count, admission)
	pool.uploadNegentropyItems(t.Context(), nip77Direction(source, relay, ids), relay)
	require.Equal(t, int32(count), frames.Load())
	require.Equal(t, uint64(2), admission.Metrics().OperationsStarted,
		"2100 events exceed one 2048-event operation and continue in a second")
}

// TestNegentropyUploadStopsAtKillSwitch: the kill switch interrupts a
// reconcile upload before its first frame; the events stay local and the
// convergent session resumes later.
func TestNegentropyUploadStopsAtKillSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	admission := nostrout.New(nostrout.Config{KillSwitchFile: path})
	pool, source, relay, frames, ids := negentropyUploadSetup(t, 50, admission)
	pool.uploadNegentropyItems(t.Context(), nip77Direction(source, relay, ids), relay)
	require.Zero(t, frames.Load(), "the kill switch must stop every uploaded frame")
	require.Zero(t, admission.Metrics().OperationsStarted)
	require.Greater(t, admission.Metrics().KillSwitchRejected, uint64(0))
}
