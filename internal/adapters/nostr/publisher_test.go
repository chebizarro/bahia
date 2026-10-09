package nostr

import (
	"context"
	"errors"
	"strings"
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

type fakeOutboxRelayPool struct {
	mu          sync.Mutex
	repo        repository.NostrEventRepository
	calls       []gonostr.Event
	responses   []fakePublishResponse
	rateLimited chan struct{}
	accepted    chan struct{}
}

type fakePublishResponse struct {
	results []PublishResult
	err     error
}

func (f *fakeOutboxRelayPool) PublishWithResults(ctx context.Context, ev gonostr.Event, _ []string) ([]PublishResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// The signed event must already be durable before the first relay attempt.
	rec, err := f.repo.GetByID(ctx, ev.ID.Hex())
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.PublishState != repository.NostrPublishStatePending {
		return nil, errors.New("event was not pending in the outbox before publish")
	}

	f.calls = append(f.calls, ev)
	response := fakePublishResponse{}
	if len(f.responses) > 0 {
		response = f.responses[0]
		f.responses = f.responses[1:]
	}
	for _, result := range response.results {
		if result.IsRateLimited() {
			select {
			case <-f.rateLimited:
			default:
				close(f.rateLimited)
			}
		}
		if result.Accepted || result.IsDuplicate() {
			select {
			case <-f.accepted:
			default:
				close(f.accepted)
			}
		}
	}
	return response.results, response.err
}

func (f *fakeOutboxRelayPool) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestPublisherSignedEventUsesDurableOutboxPath(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	fakePool := &fakeOutboxRelayPool{
		repo:        repo,
		rateLimited: make(chan struct{}),
		accepted:    make(chan struct{}),
		responses: []fakePublishResponse{{
			results: []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}},
		}},
	}
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true},
		NewRelayPool(nil, zap.NewNop()),
		repo,
		zap.NewNop(),
		WithLocalOutbox(openDeliveryTestOutbox(t), nil),
	)
	publisher.publishFn = fakePool.PublishWithResults
	publisher.relayURLs = func() []string { return []string{"wss://relay.example"} }

	event := &gonostr.Event{
		Kind:      gonostr.Kind(30315),
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "run-1"}, {"e", "loom-job-1"}, {"t", "deployment.run.health"}},
		Content:   `{"state":"stale"}`,
	}
	results, err := publisher.PublishSignedEventWithResults(ctx, event)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Accepted)

	rec, err := repo.GetByID(ctx, event.ID.Hex())
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, "deployment.run.health", rec.EntityType)
	require.Equal(t, repository.NostrPublishStatePublished, rec.PublishState)
	require.Equal(t, 1, rec.PublishAttempts)
	require.NotNil(t, rec.PublishedAt)
}

func TestPublisherEnqueueSignedEventDoesNotPublishInsideProofLease(t *testing.T) {
	outbox := openDeliveryTestOutbox(t)
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true},
		NewRelayPool(nil, zap.NewNop()),
		nil,
		zap.NewNop(),
		WithLocalOutbox(outbox, nil),
	)
	publisher.publishFn = func(context.Context, gonostr.Event, []string) ([]PublishResult, error) {
		t.Fatal("network publish must happen after the proof lease releases")
		return nil, nil
	}
	event := &gonostr.Event{Kind: gonostr.Kind(KindNIP38Status), CreatedAt: gonostr.Now(), Tags: gonostr.Tags{{"d", "run-1"}, {"t", "deployment.run.health"}}, Content: `{"state":"stale"}`}
	require.NoError(t, publisher.EnqueueSignedEvent(context.Background(), event))
	require.True(t, event.CheckID())
	require.True(t, event.VerifySignature())
	entry, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Equal(t, "deployment.run.health", entry.EntityType)
}

func TestPublisherPersistsFailedPublishAndBackgroundRetriesRateLimit(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	fakePool := &fakeOutboxRelayPool{
		repo:        repo,
		rateLimited: make(chan struct{}),
		accepted:    make(chan struct{}),
		responses: []fakePublishResponse{
			{
				results: []PublishResult{{RelayURL: "wss://relay.example", Error: errors.New("relay unavailable")}},
				err:     errors.New("all relay publishes failed"),
			},
			{
				results: []PublishResult{{RelayURL: "wss://relay.example", Reason: "rate-limited: slow down"}},
				err:     errors.New("all relay publishes failed"),
			},
			{
				results: []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}},
			},
		},
	}

	privateKey := gonostr.Generate().Hex()
	outbox := openDeliveryTestOutbox(t)
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: privateKey, PublishEnabled: true},
		NewRelayPool(nil, zap.NewNop()),
		repo,
		zap.NewNop(),
		WithLocalOutbox(outbox, nil),
	)
	publisher.publishFn = fakePool.PublishWithResults
	publisher.relayURLs = func() []string { return []string{"wss://relay.example"} }
	publisher.newBackoff = func() *Backoff {
		return &Backoff{Initial: 50 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 1, Jitter: 0}
	}
	publisher.idleInterval = time.Millisecond

	ev := &gonostr.Event{
		Kind:      gonostr.Kind(KindCASAudit),
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"t", "build.registered"}, {"d", "build-1"}},
		Content:   `{"status":"registered"}`,
	}
	_, err := publisher.PublishSignedEventWithResults(ctx, ev)
	require.ErrorIs(t, err, ErrPublishIncomplete, "a failed first round leaves the event queued")

	entry, found, err := publisher.localOutbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Equal(t, 1, entry.Rounds)
	require.Contains(t, entry.LastError, "relay unavailable")
	require.Equal(t, 1, fakePool.callCount())

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- publisher.Run(runCtx) }()

	select {
	case <-fakePool.rateLimited:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rate-limited outbox attempt")
	}
	require.Equal(t, 2, fakePool.callCount())
	require.Eventually(t, func() bool {
		e, ok, err := publisher.localOutbox.Get(ev.ID)
		return err == nil && ok && strings.Contains(e.LastError, "rate-limited: slow down")
	}, time.Second, time.Millisecond, "rate-limited result was not persisted")

	select {
	case <-fakePool.accepted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbox redelivery after backoff")
	}
	cancel()
	require.NoError(t, <-runDone)

	require.Equal(t, 3, fakePool.callCount())

	fakePool.mu.Lock()
	require.Equal(t, fakePool.calls[0].ID, fakePool.calls[1].ID)
	require.Equal(t, fakePool.calls[0].ID, fakePool.calls[2].ID)
	fakePool.mu.Unlock()

	entry, found, err = publisher.localOutbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.Equal(t, 3, entry.Rounds)
	require.Empty(t, entry.LastError)
}
