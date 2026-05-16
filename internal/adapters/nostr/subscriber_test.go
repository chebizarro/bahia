package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type memoryNostrEventRepo struct {
	mu           sync.Mutex
	records      map[string]repository.NostrEventRecord
	latest       *time.Time
	inserted     int
	failRecordID string
	failed       bool
}

func newMemoryNostrEventRepo() *memoryNostrEventRepo {
	return &memoryNostrEventRepo{records: make(map[string]repository.NostrEventRecord)}
}

func (r *memoryNostrEventRepo) Record(_ context.Context, rec *repository.NostrEventRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.ID == r.failRecordID && !r.failed {
		r.failed = true
		return false, errors.New("transient record failure")
	}
	if _, exists := r.records[rec.ID]; exists {
		return false, nil
	}
	r.records[rec.ID] = *rec
	r.inserted++
	if r.latest == nil || rec.CreatedAt.After(*r.latest) {
		latest := rec.CreatedAt
		r.latest = &latest
	}
	return true, nil
}

func (r *memoryNostrEventRepo) GetByID(_ context.Context, id string) (*repository.NostrEventRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (r *memoryNostrEventRepo) ListByKind(_ context.Context, kind int, _ int) ([]repository.NostrEventRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []repository.NostrEventRecord
	for _, rec := range r.records {
		if rec.Kind == kind {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *memoryNostrEventRepo) ListByEntity(_ context.Context, entityType string, entityID uuid.UUID, _ int) ([]repository.NostrEventRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []repository.NostrEventRecord
	for _, rec := range r.records {
		if rec.EntityType == entityType && rec.EntityID != nil && *rec.EntityID == entityID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *memoryNostrEventRepo) LatestCreatedAtForKinds(_ context.Context, kinds []int) (*time.Time, error) {
	return r.latestCreatedAt(kinds, nil), nil
}

func (r *memoryNostrEventRepo) LatestCreatedAtForKindsAndAuthors(_ context.Context, kinds []int, authors []string) (*time.Time, error) {
	return r.latestCreatedAt(kinds, authors), nil
}

func (r *memoryNostrEventRepo) latestCreatedAt(kinds []int, authors []string) *time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	kindSet := make(map[int]struct{}, len(kinds))
	for _, kind := range kinds {
		kindSet[kind] = struct{}{}
	}
	authorSet := make(map[string]struct{}, len(authors))
	for _, author := range authors {
		authorSet[author] = struct{}{}
	}
	var latest *time.Time
	for _, rec := range r.records {
		if _, ok := kindSet[rec.Kind]; !ok {
			continue
		}
		if len(authorSet) > 0 {
			if _, ok := authorSet[rec.PubKey]; !ok {
				continue
			}
		}
		if latest == nil || rec.CreatedAt.After(*latest) {
			createdAt := rec.CreatedAt
			latest = &createdAt
		}
	}
	return latest
}

func TestSubscriberBuildSubscriptionFiltersUsesPersistedAndLastSeenCursor(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	_, err := repo.Record(ctx, &repository.NostrEventRecord{
		ID:        "persisted",
		Kind:      5101,
		PubKey:    "worker",
		Content:   "{}",
		Tags:      json.RawMessage("[]"),
		Sig:       "sig",
		CreatedAt: time.Unix(100, 0).UTC(),
	})
	require.NoError(t, err)

	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5101}),
		WithBackfillLimit(25),
		withClock(func() time.Time { return time.Unix(500, 0).UTC() }),
	)

	filters, err := sub.buildSubscriptionFilters(ctx)
	require.NoError(t, err)
	require.Len(t, filters, 1)
	require.Equal(t, int64(99), int64(*filters[0].Since), "persisted cursor should be replayed with one-second overlap")
	require.Equal(t, 25, filters[0].Limit)

	sub.recordLastSeen(5101, time.Unix(125, 0).UTC())
	filters, err = sub.buildSubscriptionFilters(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(124), int64(*filters[0].Since), "in-memory cursor should win over older persisted cursor")
}

func TestSubscriberBuildSubscriptionFiltersUsesSeparateScopedCursors(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	_, err := repo.Record(ctx, &repository.NostrEventRecord{ID: "open-newer", Kind: 5101, PubKey: "worker", Content: "{}", Tags: json.RawMessage("[]"), Sig: "sig", CreatedAt: time.Unix(200, 0).UTC()})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "authorized-command", Kind: 5961, PubKey: "operator", Content: "{}", Tags: json.RawMessage("[]"), Sig: "sig", CreatedAt: time.Unix(120, 0).UTC()})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "unauthorized-command", Kind: 5961, PubKey: "other", Content: "{}", Tags: json.RawMessage("[]"), Sig: "sig", CreatedAt: time.Unix(300, 0).UTC()})
	require.NoError(t, err)

	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5101, 5961}),
		WithAuthorizedAuthors([]string{"operator"}),
		withClock(func() time.Time { return time.Unix(500, 0).UTC() }),
	)

	filters, err := sub.buildSubscriptionFilters(ctx)
	require.NoError(t, err)
	require.Len(t, filters, 2)
	require.Equal(t, []int{5101}, filters[0].Kinds)
	require.Equal(t, int64(199), int64(*filters[0].Since))
	require.Equal(t, []int{5961}, filters[1].Kinds)
	require.Equal(t, []string{"operator"}, filters[1].Authors)
	require.Equal(t, int64(119), int64(*filters[1].Since), "author-scoped cursor must ignore newer unauthorized command rows and open-kind rows")
}

func TestSubscriberBuildSubscriptionFiltersFallsBackToClockWhenNoCursor(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5101}),
		withClock(func() time.Time { return time.Unix(500, 0).UTC() }),
	)

	filters, err := sub.buildSubscriptionFilters(context.Background())
	require.NoError(t, err)
	require.Len(t, filters, 1)
	require.Equal(t, int64(500), int64(*filters[0].Since))
}

func TestSubscriberBuildSubscriptionFiltersScopesCommandKindsToAuthorizedAuthors(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5961, 31100, 5101}),
		WithAuthorizedAuthors([]string{"operator-a", "operator-b"}),
		WithBackfillLimit(10),
		withClock(func() time.Time { return time.Unix(500, 0).UTC() }),
	)

	filters, err := sub.buildSubscriptionFilters(context.Background())
	require.NoError(t, err)
	require.Len(t, filters, 2)

	open := filters[0]
	require.Equal(t, []int{5101}, open.Kinds)
	require.Empty(t, open.Authors)
	require.Equal(t, int64(500), int64(*open.Since))
	require.Equal(t, 10, open.Limit)

	scoped := filters[1]
	require.Equal(t, []int{5961, 31100}, scoped.Kinds)
	require.Equal(t, []string{"operator-a", "operator-b"}, scoped.Authors)
	require.Equal(t, int64(500), int64(*scoped.Since))
	require.Equal(t, 10, scoped.Limit)
}

func TestSubscriberHandleEventRetriesPersistenceAfterTransientRecordError(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	ev := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	repo.failRecordID = ev.ID

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, ev.ID)
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, ev)
	require.Empty(t, handled)
	require.Nil(t, repo.latest)

	sub.handleEvent(ctx, ev)
	require.Equal(t, []string{ev.ID}, handled)
	require.Equal(t, int64(105), sub.latestSeenForKinds([]int{5101}))
}

func TestSubscriberHandleEventInvokesHandlersOnlyForNewlyPersistedEvents(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	persistedEvent := signedTestEvent(t, 5101, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, &repository.NostrEventRecord{
		ID:        persistedEvent.ID,
		Kind:      persistedEvent.Kind,
		PubKey:    persistedEvent.PubKey,
		Content:   persistedEvent.Content,
		Tags:      json.RawMessage("[]"),
		Sig:       persistedEvent.Sig,
		CreatedAt: persistedEvent.CreatedAt.Time(),
	})
	require.NoError(t, err)

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, ev.ID)
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, persistedEvent)
	require.Empty(t, handled, "persisted overlap duplicate must not re-run handlers")

	newEvent := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	sub.handleEvent(ctx, newEvent)
	require.Equal(t, []string{newEvent.ID}, handled)
	require.Equal(t, int64(105), sub.latestSeenForKinds([]int{5101}))
}

func TestSubscriberHandleEventDropsInvalidBeforePersistenceAndDispatch(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	valid := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	invalid := *valid
	invalid.ID = "not-a-valid-id"

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, ev.ID)
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, &invalid)
	require.Empty(t, handled)
	require.Equal(t, 0, repo.inserted)
	require.Equal(t, int64(0), sub.latestSeenForKinds([]int{5101}))
	require.False(t, sub.dedup.IsDuplicate(valid.ID))
}
