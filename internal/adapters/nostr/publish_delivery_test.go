package nostr

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	relayA = "wss://relay-a.example"
	relayB = "wss://relay-b.example"
	relayC = "wss://relay-c.example"
)

// scriptedRelays answers each relay from its own response queue; the last
// response repeats once the queue is drained. Every call is reported on calls
// so tests advance on publish events rather than on elapsed time.
type scriptedRelays struct {
	mu        sync.Mutex
	responses map[string][]PublishResult
	calls     chan []string
}

func newScriptedRelays(responses map[string][]PublishResult) *scriptedRelays {
	return &scriptedRelays{responses: responses, calls: make(chan []string, 64)}
}

func (s *scriptedRelays) publish(_ context.Context, _ gonostr.Event, relayURLs []string) ([]PublishResult, error) {
	s.mu.Lock()
	results := make([]PublishResult, 0, len(relayURLs))
	for _, url := range relayURLs {
		queue := s.responses[url]
		result := PublishResult{Error: errors.New("unscripted relay")}
		if len(queue) > 0 {
			result = queue[0]
			if len(queue) > 1 {
				s.responses[url] = queue[1:]
			}
		}
		result.RelayURL = url
		results = append(results, result)
	}
	s.mu.Unlock()
	s.calls <- append([]string(nil), relayURLs...)
	return results, aggregatePublishResultsError(results)
}

func (s *scriptedRelays) nextCall(t *testing.T) []string {
	t.Helper()
	select {
	case call := <-s.calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a relay publish call")
		return nil
	}
}

func (s *scriptedRelays) requireNoPendingCalls(t *testing.T) {
	t.Helper()
	select {
	case call := <-s.calls:
		t.Fatalf("unexpected extra relay publish call to %v", call)
	default:
	}
}

// signalingOutbox reports outbox transitions on channels.
type signalingOutbox struct {
	*repositorytest.InMemoryNostrEventRepository
	listed    chan struct{}
	published chan string
	abandoned chan string
}

func newSignalingOutbox() *signalingOutbox {
	return &signalingOutbox{
		InMemoryNostrEventRepository: repositorytest.NewInMemoryNostrEventRepository(),
		listed:                       make(chan struct{}, 64),
		published:                    make(chan string, 8),
		abandoned:                    make(chan string, 8),
	}
}

func (o *signalingOutbox) ListUnpublishedAfter(ctx context.Context, target string, after *repository.NostrOutboxCursor, limit int) ([]repository.NostrEventRecord, error) {
	records, err := o.InMemoryNostrEventRepository.ListUnpublishedAfter(ctx, target, after, limit)
	select {
	case o.listed <- struct{}{}:
	default:
	}
	return records, err
}

func (o *signalingOutbox) MarkPublished(ctx context.Context, id string, at time.Time) error {
	err := o.InMemoryNostrEventRepository.MarkPublished(ctx, id, at)
	o.published <- id
	return err
}

func (o *signalingOutbox) AbandonPublish(ctx context.Context, id, reason string) error {
	err := o.InMemoryNostrEventRepository.AbandonPublish(ctx, id, reason)
	o.abandoned <- id
	return err
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func newDeliveryTestPublisher(t *testing.T, repo repository.NostrEventRepository, relays *scriptedRelays, quorum int, urls ...string) *Publisher {
	t.Helper()
	outbox := openDeliveryTestOutbox(t)
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true, PublishQuorum: quorum},
		NewRelayPool(nil, zap.NewNop()),
		repo,
		zap.NewNop(),
		WithLocalOutbox(outbox, nil),
	)
	publisher.publishFn = relays.publish
	publisher.relayURLs = func() []string { return urls }
	// Retries are scheduled by the delivery backoff; a tiny interval keeps the
	// test fast while every assertion still waits on a publish/outbox signal.
	publisher.newBackoff = func() *Backoff {
		return &Backoff{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1}
	}
	// Discovery polling is effectively disabled: retries must be driven by
	// the per-event schedule, not by the idle outbox poll.
	publisher.idleInterval = time.Hour
	return publisher
}

func openDeliveryTestOutbox(t *testing.T) *localstore.Outbox {
	t.Helper()
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })
	return outbox
}

// startRunner starts Run and waits for its runner to be active, after which
// the publisher keeps partially delivered events in memory for retry.
func startRunner(t *testing.T, publisher *Publisher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	require.Eventually(t, func() bool { return publisher.running.Load() },
		5*time.Second, time.Millisecond, "publisher runner did not start")
}

func testSignedEvent(content string) *gonostr.Event {
	return &gonostr.Event{
		Kind:      gonostr.Kind(30315),
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", content}, {"t", "delivery.test"}},
		Content:   content,
	}
}

func TestPublisherDefaultQuorumRelayBDownSucceedsKeepsRowPendingAndRetriesB(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {
			{Error: errors.New("connection refused")},
			{Reason: "rate-limited: slow down"},
			{Accepted: true},
		},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	startRunner(t, publisher)

	event := testSignedEvent("a-ok-b-down")
	results, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err, "default publish quorum is one relay: the caller succeeds while relay B is down")
	require.Len(t, results, 2)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State, "one relay OK must not complete delivery")
	require.Contains(t, entry.LastError, relayB)

	// Relay B is retried (and relay A is not re-sent) until B accepts.
	require.Equal(t, []string{relayB}, relays.nextCall(t))
	require.Equal(t, []string{relayB}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "event marked published after relay B accepted"))

	entry, found, err = publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.Equal(t, 3, entry.Rounds)
	require.Empty(t, entry.LastError)
	require.False(t, publisher.isTracked(event.ID.Hex()), "fully delivered events are no longer tracked")
	relays.requireNoPendingCalls(t)
}

func TestPublisherExplicitQuorumTwoWithRelayDownIsIncomplete(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Error: errors.New("connection refused")}, {Accepted: true}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 2, relayA, relayB)
	startRunner(t, publisher)

	event := testSignedEvent("quorum-two")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishIncomplete)
	var incomplete *PublishIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, 1, incomplete.Accepted)
	require.Equal(t, 2, incomplete.Required)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State, "the event stays queued below quorum")

	require.Equal(t, []string{relayB}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "event published once relay B accepted"))
	relays.requireNoPendingCalls(t)
}

func TestPublisherDefaultQuorumPermanentRejectionCompletesWithoutRetry(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Reason: "pow: difficulty 8 is less than 20"}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	startRunner(t, publisher)

	event := testSignedEvent("pow-reject")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "delivery complete: A accepted, B terminal"))
	require.False(t, publisher.isTracked(event.ID.Hex()))
	relays.requireNoPendingCalls(t)
}

func TestPublisherPermanentRejectionStopsRetriesForThatRelayOnly(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Reason: "invalid: bad signature"}},
		relayC: {{Error: errors.New("timeout")}, {Accepted: true}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 2, relayA, relayB, relayC)
	startRunner(t, publisher)

	event := testSignedEvent("permanent-reject")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishIncomplete)
	require.ElementsMatch(t, []string{relayA, relayB, relayC}, relays.nextCall(t))

	// Only relay C is retried: relay B's invalid: rejection is terminal.
	require.Equal(t, []string{relayC}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "event published once quorum reached and every relay settled"))
	relays.requireNoPendingCalls(t)

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPublished, entry.State)
}

func TestPublisherPermanentRejectionMakingQuorumUnreachableAbandonsWithoutRetry(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Reason: "blocked: pubkey not allowed"}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, config.PublishQuorumAllRelays, relayA, relayB)
	startRunner(t, publisher)

	event := testSignedEvent("blocked")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishAbandoned, "an unreachable quorum is abandoned, not queued")
	require.NotErrorIs(t, err, ErrPublishIncomplete)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.abandoned, "event abandoned"))

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxFailed, entry.State, "abandoned rows leave the outbox as failed")
	require.Contains(t, entry.LastError, "abandoned")
	require.Contains(t, entry.LastError, "blocked: pubkey not allowed")
	depth, err := outbox.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Zero(t, depth)
	require.False(t, publisher.isTracked(event.ID.Hex()))
	relays.requireNoPendingCalls(t)
}

func TestPublisherQuorumMetReturnsDeliveredAndStillRetriesRemainder(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Reason: "duplicate: already have this event"}},
		relayB: {{Error: errors.New("connection reset")}, {Accepted: true}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	startRunner(t, publisher)

	event := testSignedEvent("quorum")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err, "duplicate OK from relay A meets the default quorum of 1")
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))

	require.Equal(t, []string{relayB}, relays.nextCall(t), "relay B is still retried after quorum")
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "event published after remainder settled"))
	relays.requireNoPendingCalls(t)
}

func TestPublisherAbandonsAfterAttemptBudgetWhenQuorumNeverMet(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Error: errors.New("relay down")}},
		relayB: {{Error: errors.New("relay down")}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	publisher.maxAttempts = 3
	startRunner(t, publisher)

	event := testSignedEvent("budget")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.ErrorIs(t, err, ErrPublishIncomplete)
	for range 3 {
		require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	}
	require.Equal(t, event.ID.Hex(), receive(t, outbox.abandoned, "event abandoned after budget"))

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxFailed, entry.State)
	require.Contains(t, entry.LastError, "abandoned after 3 publish attempts")
	require.Equal(t, 3, entry.Rounds)
	relays.requireNoPendingCalls(t)
}

func TestPublisherBudgetExhaustedAfterQuorumPublishesRow(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Error: errors.New("relay down")}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	publisher.maxAttempts = 3
	startRunner(t, publisher)

	event := testSignedEvent("budget-after-quorum")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	require.Equal(t, []string{relayB}, relays.nextCall(t))
	require.Equal(t, []string{relayB}, relays.nextCall(t))
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "row published once relay B's budget ran out"))
	relays.requireNoPendingCalls(t)
}

func TestPublisherRequiredAcceptances(t *testing.T) {
	cases := []struct {
		quorum, configured, want int
	}{
		{0, 3, 1},
		{1, 3, 1},
		{2, 3, 2},
		{5, 3, 3},
		{config.PublishQuorumAllRelays, 3, 3},
		{0, 0, 0},
	}
	for _, tc := range cases {
		publisher := &Publisher{quorum: tc.quorum}
		require.Equal(t, tc.want, publisher.requiredAcceptances(tc.configured), "quorum=%d configured=%d", tc.quorum, tc.configured)
	}
}

func TestPublisherRunnerDiscoversPendingRowsPastABlockedPage(t *testing.T) {
	outbox := newSignalingOutbox()
	stuck := testSignedEvent("stuck")
	fresh := testSignedEvent("fresh")
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA)
	publisher.pageSize = 1

	// Two entries enqueued pending by another producer (no inline publish).
	for i, ev := range []*gonostr.Event{stuck, fresh} {
		require.NoError(t, signEventWithPrivateKeyHex(ev, publisher.privateKey))
		_, err := publisher.localOutbox.Enqueue(localstore.OutboxEntry{
			Event:      *ev,
			Target:     publisher.target,
			EnqueuedAt: time.Unix(int64(1000+i), 0).UTC(),
		})
		require.NoError(t, err)
	}

	startRunner(t, publisher)
	published := map[string]bool{}
	published[receive(t, outbox.published, "first discovered row published")] = true
	published[receive(t, outbox.published, "second discovered row published")] = true
	require.True(t, published[stuck.ID.Hex()])
	require.True(t, published[fresh.ID.Hex()])
}

func TestPublisherWithoutRelaysIsNotDelivered(t *testing.T) {
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(nil)
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0)

	event := testSignedEvent("no-relays")
	_, err := publisher.PublishSignedEventWithResults(context.Background(), event)
	require.ErrorIs(t, err, ErrPublishIncomplete)
	require.ErrorContains(t, err, "no write relays configured")
	rec, err := outbox.GetByID(context.Background(), event.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStatePending, rec.PublishState)
}

func TestClassifyPublishResult(t *testing.T) {
	cases := []struct {
		result PublishResult
		want   relayPublishOutcome
	}{
		{PublishResult{Accepted: true}, relayPublishAccepted},
		{PublishResult{Reason: "duplicate: have it"}, relayPublishAccepted},
		{PublishResult{Reason: "blocked: no"}, relayPublishRejected},
		{PublishResult{Reason: "invalid: bad id"}, relayPublishRejected},
		{PublishResult{Reason: "pow: difficulty 8 < 20"}, relayPublishRejected},
		{PublishResult{Reason: "rate-limited: slow"}, relayPublishRetry},
		{PublishResult{Reason: "auth-required: sign in"}, relayPublishRetry},
		{PublishResult{Reason: "error: internal"}, relayPublishRetry},
		{PublishResult{Error: errors.New("dial tcp: refused")}, relayPublishRetry},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, classifyPublishResult(tc.result), "%+v", tc.result)
	}
}

// Rounds that only hit the pool's fail-fast reconnect backoff for a dial
// failure the event has already counted do not consume the attempt budget and
// are not recorded; a fresh dial failure does count.
func TestPublisherFailFastBackoffRoundsDoNotConsumeAttemptBudget(t *testing.T) {
	ctx := context.Background()
	outbox := newSignalingOutbox()
	failedAt := time.Unix(1_800_000_000, 0)
	backoff := func(at time.Time) PublishResult {
		// RetryAt in the past keeps the retries immediate in this test.
		return PublishResult{Error: &RelayReconnectBackoffError{RelayURL: relayB, RetryAt: time.Unix(1, 0), FailedAt: at, LastErr: errors.New("connection refused")}}
	}
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {
			backoff(failedAt),                                                                             // inline round: relay A is contacted, counts (1)
			backoff(failedAt), backoff(failedAt), backoff(failedAt), backoff(failedAt), backoff(failedAt), // same failure: skipped
			backoff(failedAt.Add(time.Minute)), // a fresh dial failure counts (2)
			{Accepted: true},                   // counts (3)
		},
	})
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA, relayB)
	publisher.maxAttempts = 3
	startRunner(t, publisher)

	event := testSignedEvent("fail-fast-budget")
	_, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{relayA, relayB}, relays.nextCall(t))
	for range 7 {
		require.Equal(t, []string{relayB}, relays.nextCall(t))
	}
	require.Equal(t, event.ID.Hex(), receive(t, outbox.published, "relay B accepted within the budget"))

	entry, found, err := publisher.localOutbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.Empty(t, entry.LastError, "relay B accepted: nothing was given up on")
	require.Equal(t, 3, entry.Rounds, "only rounds that contacted a relay or saw a fresh dial failure count")
	relays.requireNoPendingCalls(t)
}
