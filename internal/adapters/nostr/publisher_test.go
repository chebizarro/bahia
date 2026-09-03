package nostr

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
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

func (f *fakeOutboxRelayPool) PublishWithResults(ctx context.Context, ev gonostr.Event) ([]PublishResult, error) {
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
	repo := repository.NewInMemoryNostrEventRepository()
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
	)
	publisher.publishFn = fakePool.PublishWithResults

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

func TestPublisherPersistsFailedPublishAndBackgroundRetriesRateLimit(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
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
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: privateKey, PublishEnabled: true},
		NewRelayPool(nil, zap.NewNop()),
		repo,
		zap.NewNop(),
	)
	publisher.publishFn = fakePool.PublishWithResults
	publisher.newBackoff = func() *Backoff {
		return &Backoff{Initial: 50 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 1, Jitter: 0}
	}
	publisher.idleInterval = time.Millisecond

	publisher.publishEvent(ctx, KindBuildRegistered, "build.registered", events.Event{
		EntityID: "build-1",
		Data:     map[string]any{"status": "registered"},
	})

	pending, err := repo.ListUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, repository.NostrPublishStatePending, pending[0].PublishState)
	require.Equal(t, 1, pending[0].PublishAttempts)
	require.Contains(t, pending[0].LastPublishError, "relay unavailable")
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
	var rec *repository.NostrEventRecord
	require.Eventually(t, func() bool {
		rec, err = repo.GetByID(ctx, pending[0].ID)
		return err == nil && rec != nil && strings.Contains(rec.LastPublishError, "rate-limited: slow down")
	}, time.Second, time.Millisecond, "rate-limited result was not persisted")

	select {
	case <-fakePool.accepted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbox redelivery after backoff")
	}
	cancel()
	require.NoError(t, <-runDone)

	require.Equal(t, 3, fakePool.callCount())
	pending, err = repo.ListUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	fakePool.mu.Lock()
	eventID := fakePool.calls[0].ID.Hex()
	require.Equal(t, fakePool.calls[0].ID, fakePool.calls[1].ID)
	require.Equal(t, fakePool.calls[0].ID, fakePool.calls[2].ID)
	fakePool.mu.Unlock()

	rec, err = repo.GetByID(ctx, eventID)
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, repository.NostrPublishStatePublished, rec.PublishState)
	require.Equal(t, 3, rec.PublishAttempts)
	require.Empty(t, rec.LastPublishError)
	require.NotNil(t, rec.PublishedAt)
}
