package nostr

import (
	"context"
	"errors"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func pendingOutboxRecord(t *testing.T, repo *repository.InMemoryNostrEventRepository, enqueuedAt time.Time) gonostr.Event {
	t.Helper()
	ev := gonostr.Event{Kind: gonostr.Kind(1), CreatedAt: gonostr.Timestamp(enqueuedAt.Add(-24 * time.Hour).Unix()), Content: t.Name() + enqueuedAt.String()}
	require.NoError(t, ev.Sign(gonostr.Generate()))
	rec := nostrEventRecordFromEvent(ev, "test")
	rec.ReceivedAt = enqueuedAt
	rec.PublishState = repository.NostrPublishStatePending
	_, err := repo.Record(t.Context(), rec)
	require.NoError(t, err)
	return ev
}

func newExpiryTestPublisher(repo repository.NostrEventRepository, now time.Time, publish func(context.Context, gonostr.Event) ([]PublishResult, error)) *Publisher {
	publisher := NewPublisher(
		config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true},
		NewRelayPool(nil, zap.NewNop(), WithOutboundAdmission(newIsolatedTestAdmission())),
		repo,
		zap.NewNop(),
		WithPublishRetryLifetime(time.Hour),
	)
	publisher.now = func() time.Time { return now }
	publisher.publishFn = publish
	return publisher
}

func TestOutboxExpiresEventsPastRetryLifetimeWithoutSending(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	repo := repository.NewInMemoryNostrEventRepository()
	stale := pendingOutboxRecord(t, repo, now.Add(-61*time.Minute))
	fresh := pendingOutboxRecord(t, repo, now.Add(-5*time.Minute))
	var sent []string
	publisher := newExpiryTestPublisher(repo, now, func(_ context.Context, ev gonostr.Event) ([]PublishResult, error) {
		sent = append(sent, ev.ID.Hex())
		return []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}}, nil
	})

	pending, failed, _, err := publisher.retryUnpublished(t.Context())
	require.NoError(t, err)
	require.False(t, failed)
	require.Equal(t, 1, pending)
	require.Equal(t, []string{fresh.ID.Hex()}, sent, "an expired event must never be sent again")

	rec, err := repo.GetByID(t.Context(), stale.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStateExpired, rec.PublishState)
	require.Contains(t, rec.LastPublishError, "retry lifetime expired")

	// Expiry is durable and happens once: a later sweep selects nothing.
	sent = nil
	pending, _, _, err = publisher.retryUnpublished(t.Context())
	require.NoError(t, err)
	require.Zero(t, pending)
	require.Empty(t, sent)
	count, err := repo.CountUnpublished(t.Context())
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestOutboxRetryLifetimeIsMeasuredFromEnqueueNotCreatedAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	repo := repository.NewInMemoryNostrEventRepository()
	// pendingOutboxRecord backdates created_at by a day, as NIP-59 wraps do.
	ev := pendingOutboxRecord(t, repo, now.Add(-10*time.Minute))
	var deadline time.Time
	publisher := newExpiryTestPublisher(repo, now, func(ctx context.Context, _ gonostr.Event) ([]PublishResult, error) {
		deadline, _ = ctx.Deadline()
		return []PublishResult{{RelayURL: "wss://relay.example", Accepted: true}}, nil
	})
	_, _, _, err := publisher.retryUnpublished(t.Context())
	require.NoError(t, err)
	require.Equal(t, now.Add(50*time.Minute), deadline, "the attempt is bounded by the remaining retry lifetime")
	rec, err := repo.GetByID(t.Context(), ev.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStatePublished, rec.PublishState)
}

func TestOutboxStopsBatchOnAdmissionRejectionAndPreservesCause(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	repo := repository.NewInMemoryNostrEventRepository()
	for i := 0; i < 5; i++ {
		pendingOutboxRecord(t, repo, now.Add(-time.Duration(i+1)*time.Minute))
	}
	calls := 0
	publisher := newExpiryTestPublisher(repo, now, func(context.Context, gonostr.Event) ([]PublishResult, error) {
		calls++
		return nil, nostrout.ErrBudgetExceeded
	})
	_, failed, _, err := publisher.retryUnpublished(t.Context())
	require.True(t, failed)
	require.ErrorIs(t, err, ErrOutboundBudgetExceeded, "the admission cause must survive outbox error wrapping")
	require.Equal(t, 1, calls, "a shared-controller refusal must not be repeated for every pending row")
}

func TestPublisherRetryLifetimeCannotBeDisabled(t *testing.T) {
	publisher := NewPublisher(config.NostrConfig{}, NewRelayPool(nil, zap.NewNop()), repository.NewInMemoryNostrEventRepository(), zap.NewNop(), WithPublishRetryLifetime(0), WithPublishRetryLifetime(-time.Second))
	require.Equal(t, DefaultPublishRetryLifetime, publisher.retryLifetime)
	require.True(t, errors.Is(&outboxPublishError{message: "x", cause: nostrout.ErrKillSwitch}, ErrOutboundKillSwitch))
}
