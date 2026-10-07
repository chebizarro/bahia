package nostr

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The publish outbox without PostgreSQL (bahia-irsry.10.4): in-process khatru
// relays answer every EVENT with OK, and every wait is on a delivery round
// (an OK-driven publish call), a delivery hook, or the runner's exit after
// finishing a round. Nothing sleeps.

// localOutboxRound is one delivery round: the relays contacted and their OKs.
type localOutboxRound struct {
	targets []string
	results []PublishResult
}

type localOutboxHarness struct {
	pub       *Publisher
	outbox    *localstore.Outbox
	store     *localstore.Store
	rounds    chan localOutboxRound
	delivered chan gonostr.Event
	abandoned chan gonostr.Event
	stopRun   func()
}

// newLocalOutboxHarness builds a publisher with no PostgreSQL over the outbox
// and event store files under dir, publishing to pool with the given quorum.
func newLocalOutboxHarness(t *testing.T, dir string, pool *RelayPool, key string, quorum int) *localOutboxHarness {
	t.Helper()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	store, err := localstore.Open(filepath.Join(dir, "daemon.bolt"))
	require.NoError(t, err)
	h := &localOutboxHarness{
		outbox:    outbox,
		store:     store,
		rounds:    make(chan localOutboxRound, 64),
		delivered: make(chan gonostr.Event, 16),
		abandoned: make(chan gonostr.Event, 16),
	}
	h.pub = NewPublisher(config.NostrConfig{PrivateKey: key, PublishEnabled: true, PublishQuorum: quorum}, pool, nil, zap.NewNop(),
		WithLocalOutbox(outbox, store))
	h.pub.newBackoff = func() *Backoff { return &Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2} }
	publish := h.pub.publishFn
	h.pub.publishFn = func(ctx context.Context, ev gonostr.Event, urls []string) ([]PublishResult, error) {
		results, err := publish(ctx, ev, urls)
		h.rounds <- localOutboxRound{targets: append([]string(nil), urls...), results: append([]PublishResult(nil), results...)}
		return results, err
	}
	h.pub.OnDelivered(func(ev gonostr.Event) { h.delivered <- ev })
	h.pub.OnDeliveryAbandoned(func(ev gonostr.Event) { h.abandoned <- ev })
	t.Cleanup(func() { h.close() })
	return h
}

// startRunner runs the publisher's retry runner until stopRun, which returns
// once the runner has finished the round it was in.
func (h *localOutboxHarness) startRunner() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.pub.Run(ctx)
	}()
	h.stopRun = func() {
		cancel()
		<-done
	}
}

func (h *localOutboxHarness) close() {
	if h.stopRun != nil {
		h.stopRun()
		h.stopRun = nil
	}
	_ = h.outbox.Close()
	_ = h.store.Close()
}

// waitAccepted waits for the round in which relayURL accepts.
func (h *localOutboxHarness) waitAccepted(t *testing.T, relayURL string) localOutboxRound {
	t.Helper()
	for {
		round := receive(t, h.rounds, "a delivery round accepted by "+relayURL)
		for _, result := range round.results {
			if result.RelayURL == relayURL && (result.Accepted || result.IsDuplicate()) {
				return round
			}
		}
	}
}

func (h *localOutboxHarness) entry(t *testing.T, id gonostr.ID) localstore.OutboxEntry {
	t.Helper()
	entry, found, err := h.outbox.Get(id)
	require.NoError(t, err)
	require.True(t, found)
	return entry
}

func (h *localOutboxHarness) storeHolds(id gonostr.ID) bool {
	for range h.store.QueryEvents(gonostr.Filter{IDs: []gonostr.ID{id}}) {
		return true
	}
	return false
}

func localOutboxEvent(content string) *gonostr.Event {
	return &gonostr.Event{Kind: KindCASAudit, CreatedAt: gonostr.Now(), Tags: gonostr.Tags{{"t", "outbox-test"}}, Content: content}
}

func rejectEvents(relay *syncTestRelay, reason string) {
	relay.relay.OnEvent = func(context.Context, gonostr.Event) (bool, string) { return true, reason }
}

// Quorum 1 with one relay down: the caller succeeds on the first relay's OK,
// OnDelivered reports it, the runner keeps retrying the down relay until it
// accepts, and only then does the entry settle as published.
func TestLocalOutboxWithoutPostgresRetriesDownRelayUntilAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	a := startSyncTestRelay(t, syncTestRelayOptions{})
	b := startSyncTestRelay(t, syncTestRelayOptions{})
	b.down.Store(true)
	pool := newSyncTestPool(a, b)
	defer pool.Close()
	h := newLocalOutboxHarness(t, t.TempDir(), pool, gonostr.Generate().Hex(), 0)
	h.startRunner()

	ev := localOutboxEvent("retry the down relay")
	_, err := h.pub.PublishSignedEventWithResults(ctx, ev)
	require.NoError(t, err, "the default quorum of one relay accepted")
	require.Equal(t, ev.ID, receive(t, h.delivered, "the delivered hook").ID)
	outcome, err := h.pub.DeliveryOutcome(ctx, ev.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, nostrutil.DeliveryDelivered, outcome)
	require.True(t, h.storeHolds(ev.ID), "the daemon's own output is kept in the local event store")
	counts, err := h.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Pending, "pending until the down relay has accepted too")

	b.down.Store(false)
	h.waitAccepted(t, b.url)
	h.stopRun()
	h.stopRun = nil
	entry := h.entry(t, ev.ID)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.True(t, entry.Relays[a.url].Accepted)
	require.True(t, entry.Relays[b.url].Accepted)
	require.True(t, a.has(ev.ID))
	require.True(t, b.has(ev.ID))
	counts, err = h.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, localstore.OutboxCounts{}, counts)
	select {
	case again := <-h.delivered:
		t.Fatalf("delivery of %s reported twice by one process", again.ID.Hex())
	default:
	}
}

// A permanent rejection ends retries for that relay only; when it leaves the
// quorum unreachable the first round abandons the event: the caller gets
// ErrPublishAbandoned, OnDeliveryAbandoned fires, the entry is failed and the
// event no longer counts as the daemon's output (the caller was told; §3.7
// flags only events abandoned after the caller was told they were queued).
func TestLocalOutboxWithoutPostgresPermanentRejectionAndAbandonment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	a := startSyncTestRelay(t, syncTestRelayOptions{})
	blocked := startSyncTestRelay(t, syncTestRelayOptions{})
	rejectEvents(blocked, "blocked: not on the allow list")
	pool := newSyncTestPool(a, blocked)
	defer pool.Close()
	key := gonostr.Generate().Hex()

	t.Run("quorum met despite the rejection", func(t *testing.T) {
		h := newLocalOutboxHarness(t, t.TempDir(), pool, key, 0)
		ev := localOutboxEvent("one relay blocks")
		_, err := h.pub.PublishSignedEventWithResults(ctx, ev)
		require.NoError(t, err)
		receive(t, h.delivered, "the delivered hook")
		entry := h.entry(t, ev.ID)
		require.Equal(t, localstore.OutboxPublished, entry.State, "nothing is left to retry: the rejecting relay is terminal")
		require.True(t, strings.HasPrefix(entry.Relays[blocked.url].Rejected, "blocked:"))
		require.Equal(t, 1, entry.Rounds)
	})

	t.Run("quorum unreachable", func(t *testing.T) {
		h := newLocalOutboxHarness(t, t.TempDir(), pool, key, config.PublishQuorumAllRelays)
		ev := localOutboxEvent("every relay required")
		_, err := h.pub.PublishSignedEventWithResults(ctx, ev)
		require.ErrorIs(t, err, nostrutil.ErrPublishAbandoned)
		require.False(t, nostrutil.IsPublishQueued(err))
		require.Equal(t, ev.ID, receive(t, h.abandoned, "the abandoned hook").ID)
		entry := h.entry(t, ev.ID)
		require.Equal(t, localstore.OutboxFailed, entry.State)
		require.Contains(t, entry.LastError, "abandoned: required relay acceptance is unreachable")
		require.False(t, h.storeHolds(ev.ID), "an event abandoned in the caller's round is not the daemon's output")
		_, found, err := h.store.Undelivered(*ev)
		require.NoError(t, err)
		require.False(t, found, "nothing to flag: the caller was told")
		outcome, err := h.pub.DeliveryOutcome(ctx, ev.ID.Hex())
		require.NoError(t, err)
		require.Equal(t, nostrutil.DeliveryAbandoned, outcome)
		counts, err := h.outbox.Counts()
		require.NoError(t, err)
		require.Equal(t, localstore.OutboxCounts{Failed: 1}, counts)
		select {
		case ev := <-h.delivered:
			t.Fatalf("abandoned event %s reported delivered", ev.ID.Hex())
		default:
		}
	})
}

// A relay that never accepts uses up the attempt budget; the runner then
// abandons the event (OK-scripted rounds, so the budget is spent quickly).
func TestLocalOutboxWithoutPostgresAbandonsAfterAttemptBudget(t *testing.T) {
	relays := newScriptedRelays(map[string][]PublishResult{
		relayA: {{Accepted: true}},
		relayB: {{Reason: "error: try later"}},
	})
	publisher := newDeliveryTestPublisher(t, nil, relays, config.PublishQuorumAllRelays, relayA, relayB)
	dir := t.TempDir()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })
	WithLocalOutbox(outbox, nil)(publisher)
	publisher.maxAttempts = 3
	abandoned := make(chan gonostr.Event, 1)
	publisher.OnDeliveryAbandoned(func(ev gonostr.Event) { abandoned <- ev })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = publisher.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	ev := localOutboxEvent("never accepted by b")
	_, err = publisher.PublishSignedEventWithResults(t.Context(), ev)
	require.ErrorIs(t, err, nostrutil.ErrPublishIncomplete, "queued below the all-relays quorum")
	require.Equal(t, ev.ID, receive(t, abandoned, "abandonment after the attempt budget").ID)
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxFailed, entry.State)
	require.Equal(t, 3, entry.Rounds)
	require.Contains(t, entry.LastError, "abandoned after 3 publish attempts")
}

// After a restart the runner resumes a pending entry from the local outbox:
// it contacts only the relay that had not accepted (the per-relay state is
// durable, so the relay that accepted is not resent to), settles the entry
// and reports the delivery again (at least once).
func TestLocalOutboxWithoutPostgresRestartResumesPendingDeliveries(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	a := startSyncTestRelay(t, syncTestRelayOptions{})
	b := startSyncTestRelay(t, syncTestRelayOptions{})
	b.down.Store(true)
	dir := t.TempDir()
	key := gonostr.Generate().Hex()

	firstPool := newSyncTestPool(a, b)
	first := newLocalOutboxHarness(t, dir, firstPool, key, 0)
	ev := localOutboxEvent("survives a restart")
	_, err := first.pub.PublishSignedEventWithResults(ctx, ev)
	require.NoError(t, err)
	receive(t, first.delivered, "the delivered hook before the restart")
	first.close()
	firstPool.Close()

	b.down.Store(false)
	secondPool := newSyncTestPool(a, b)
	defer secondPool.Close()
	second := newLocalOutboxHarness(t, dir, secondPool, key, 0)
	pending, err := second.outbox.ListPending("", nil, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.True(t, pending[0].Relays[a.url].Accepted)
	require.True(t, pending[0].Delivered)

	second.startRunner()
	round := second.waitAccepted(t, b.url)
	require.Equal(t, []string{b.url}, round.targets, "the relay that accepted before the restart is not resent to")
	require.Equal(t, ev.ID, receive(t, second.delivered, "the delivery reported again after the restart").ID)
	second.stopRun()
	second.stopRun = nil
	entry := second.entry(t, ev.ID)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.True(t, b.has(ev.ID))
}
