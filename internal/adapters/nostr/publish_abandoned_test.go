package nostr

import (
	"context"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// abandonedRecorder is an OnDeliveryAbandoned handler that reports each
// abandoned event id on a channel.
func abandonedRecorder(ch chan<- string) func(gonostr.Event) {
	return func(ev gonostr.Event) { ch <- ev.ID.Hex() }
}

// Every outbox entry point reports a first-round abandonment (every relay
// answered OK=false with a permanent prefix) as ErrPublishAbandoned, never as
// the queued ErrPublishIncomplete, and the row is failed.
func TestPublishEntryPointsReportFirstRoundAbandonment(t *testing.T) {
	entryPoints := map[string]func(context.Context, *Publisher, *gonostr.Event) error{
		"PublishSignedEvent": func(ctx context.Context, p *Publisher, ev *gonostr.Event) error {
			return p.PublishSignedEvent(ctx, ev)
		},
		"PublishSignedEventWithResults": func(ctx context.Context, p *Publisher, ev *gonostr.Event) error {
			results, err := p.PublishSignedEventWithResults(ctx, ev)
			require.Len(t, results, 2, "per-relay OK results are still returned")
			return err
		},
		"PublishPresignedEvent": func(ctx context.Context, p *Publisher, ev *gonostr.Event) error {
			require.NoError(t, ev.Sign(gonostr.Generate()))
			_, err := p.PublishPresignedEvent(ctx, *ev, "config-fabric.desired")
			return err
		},
		"PublishProjection": func(ctx context.Context, p *Publisher, ev *gonostr.Event) error {
			require.NoError(t, ev.Sign(gonostr.Generate()))
			id := uuid.New()
			return p.PublishProjection(ctx, *ev, "service", &id)
		},
	}
	for name, publish := range entryPoints {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			outbox := newSignalingOutbox()
			relays := newScriptedRelays(map[string][]PublishResult{
				relayA: {{Reason: "blocked: pubkey not allowed"}},
				relayB: {{Reason: "invalid: bad tag"}},
			})
			publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
			abandoned := make(chan string, 4)
			publisher.OnDeliveryAbandoned(abandonedRecorder(abandoned))
			startRunner(t, publisher)

			event := testSignedEvent("abandoned-" + name)
			err := publish(ctx, publisher, event)
			require.ErrorIs(t, err, ErrPublishAbandoned)
			require.True(t, nostrutil.IsPublishAbandoned(err))
			require.False(t, nostrutil.IsPublishQueued(err), "an abandoned publish must not read as queued")
			var detail *PublishAbandonedError
			require.True(t, errors.As(err, &detail))
			require.Equal(t, event.ID.Hex(), detail.EventID)
			require.Equal(t, 0, detail.Accepted)
			require.Equal(t, 1, detail.Required)
			require.Contains(t, detail.Detail, "blocked: pubkey not allowed")

			require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
			require.Equal(t, event.ID.Hex(), receive(t, outbox.abandoned, "row abandoned"))
			require.Equal(t, event.ID.Hex(), receive(t, abandoned, "abandonment handler"))
			rec, err := outbox.GetByID(ctx, event.ID.Hex())
			require.NoError(t, err)
			require.Equal(t, repository.NostrPublishStateFailed, rec.PublishState)
			require.False(t, publisher.isTracked(event.ID.Hex()))
			relays.requireNoPendingCalls(t)
		})
	}
}

// An event the caller was told is queued, and that the runner abandons in a
// later round, is reported to every registered handler: the first-round error
// was ErrPublishIncomplete, so the handlers are the only terminal signal.
func TestPublisherRunnerAbandonmentNotifiesEveryHandler(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Error: errors.New("connection refused")}, {Reason: "blocked: pubkey not allowed"}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA)
	first := make(chan string, 4)
	second := make(chan string, 4)
	publisher.OnDeliveryAbandoned(abandonedRecorder(first))
	publisher.OnDeliveryAbandoned(nil) // ignored, does not clear
	publisher.OnDeliveryAbandoned(abandonedRecorder(second))
	startRunner(t, publisher)

	event := testSignedEvent("later-abandoned")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishIncomplete, "the first round leaves the event queued")
	require.NotErrorIs(t, err, ErrPublishAbandoned)
	require.Equal(t, []string{relayA}, relays.nextCall(t))

	require.Equal(t, []string{relayA}, relays.nextCall(t), "the runner retries the relay")
	require.Equal(t, event.ID.Hex(), receive(t, outbox.abandoned, "row abandoned by the runner"))
	require.Equal(t, event.ID.Hex(), receive(t, first, "first handler"))
	require.Equal(t, event.ID.Hex(), receive(t, second, "second handler"))
	rec, err := outbox.GetByID(ctx, event.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStateFailed, rec.PublishState)
	require.Contains(t, rec.LastPublishError, "blocked: pubkey not allowed")
	relays.requireNoPendingCalls(t)
}

// A caller that reaches a delivery another round has just abandoned (before
// it is forgotten) gets the abandonment, not a nil "success".
func TestSettledAbandonedDeliveryReportsAbandonedToLateCaller(t *testing.T) {
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{relayA: {{Reason: "blocked: no"}}})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA)
	event := testSignedEvent("settled")
	require.NoError(t, event.Sign(gonostr.Generate()))

	// The event must be in the local outbox before deliverRound can persist
	// its outcome (: local outbox is the required ledger).
	_, err := publisher.localOutbox.Enqueue(localstore.OutboxEntry{Event: *event, Target: publisher.target, EnqueuedAt: publisher.now()})
	require.NoError(t, err)

	d, _ := publisher.trackDelivery(*event, 0)
	d.mu.Lock()
	first := publisher.deliverRound(context.Background(), d)
	late := publisher.deliverRound(context.Background(), d)
	d.mu.Unlock()
	require.ErrorIs(t, first.err, ErrPublishAbandoned)
	require.ErrorIs(t, late.err, ErrPublishAbandoned)
	require.True(t, late.settled)
	require.False(t, late.delivered)
}

// A runner discovery pass that lists a freshly recorded row while its first
// inline round is still in flight must not start a second delivery: the
// delivery is tracked before the row is durable.
func TestEnqueueTracksDeliveryBeforeRowIsDiscoverable(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{relayA: {{Accepted: true}}})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA)
	publisher.running.Store(true)
	event := testSignedEvent("tracked-first")
	require.NoError(t, event.Sign(gonostr.Generate()))

	// Discovery runs from inside Record, i.e. once the row is listable but
	// before the inline round has started.
	var discoveredDuringRecord bool
	recorder := &recordHookOutbox{signalingOutbox: outbox, afterRecord: func() {
		more, err := publisher.discoverPending(ctx)
		require.NoError(t, err)
		require.False(t, more)
		discoveredDuringRecord = true
	}}
	publisher.eventRepo = recorder
	publisher.outboxRepo = recorder
	publisher.archive = newPostgresArchive(recorder, zap.NewNop())

	attempt, err := publisher.enqueueAndDeliver(ctx, *event, "delivery.test", nil)
	require.NoError(t, err)
	require.NoError(t, attempt.err)
	require.True(t, discoveredDuringRecord)
	require.Equal(t, []string{relayA}, relays.nextCall(t), "only the inline round contacts the relay")
	relays.requireNoPendingCalls(t)
}

// recordHookOutbox runs afterRecord once a row has been recorded.
type recordHookOutbox struct {
	*signalingOutbox
	afterRecord func()
}

func (o *recordHookOutbox) Record(ctx context.Context, rec *repository.NostrEventRecord) (bool, error) {
	inserted, err := o.signalingOutbox.Record(ctx, rec)
	if err == nil && o.afterRecord != nil {
		o.afterRecord()
	}
	return inserted, err
}

// With a local outbox, per-relay state is durable: an event published before
// Run is active has relay A marked accepted in the outbox entry, so the
// runner's discovery pass restores that state and retries only relay B.
func TestPublishBeforeRunnerActiveIsRetriedOnlyForFailedRelays(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Error: errors.New("connection refused")}, {Accepted: true}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)

	event := testSignedEvent("before-runner")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err, "relay A meets the quorum")
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	require.False(t, publisher.isTracked(event.ID.Hex()), "no active runner: the durable row carries the retry")

	startRunner(t, publisher)
	require.Equal(t, []string{relayB}, relays.nextCall(t), "discovery retries only relay B (relay A already accepted, state is durable)")
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "row published"))
	relays.requireNoPendingCalls(t)
}
