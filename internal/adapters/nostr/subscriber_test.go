package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
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

func (r *memoryNostrEventRepo) ListByKinds(_ context.Context, wanted []int, _ int) ([]repository.NostrEventRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kindSet := make(map[int]struct{}, len(wanted))
	for _, kind := range wanted {
		kindSet[kind] = struct{}{}
	}
	var out []repository.NostrEventRecord
	for _, rec := range r.records {
		if _, ok := kindSet[rec.Kind]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *memoryNostrEventRepo) FindByTag(_ context.Context, _ string, _ string, wanted []int, limit int) ([]repository.NostrEventRecord, error) {
	return r.ListByKinds(context.Background(), wanted, limit)
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

func TestDefaultInboundKindsContainsOnlyOperationalNonReactorStreams(t *testing.T) {
	expected := []int{
		kinds.ConfigACLList,
		kinds.ConfigPolicy,
		KindCASControlState,
		KindCASAudit,
		KindNIP38Status,
		kinds.AssistantTranscript,
		kinds.SoulFactoryRuntimeCapability,
		kinds.ContextVMToolsList,
		kinds.ContextVMResourcesList,
		kinds.ContextVMResourceTemplatesList,
		kinds.ContextVMPromptsList,
		KindRelaySetDiscovery,
		KindNIP65RelayList,
		KindHiveCIWorkflowRun,
		KindHiveCIWorkflowResult,
		KindLoomWorkerAdvertisement,
		KindLoomJobStatusUpdate,
		KindLoomJobResult,
		KindLoomJobCancellation,
	}
	require.ElementsMatch(t, expected, DefaultInboundKinds)
	for _, kind := range DefaultInboundKinds {
		require.False(t, isCanonicalControlPlaneRequest(kind), "subscriber default kind %d must not duplicate reactor-owned control-plane traffic", kind)
		require.False(t, isLegacyRuntimeKind(kind), "subscriber default kind %d must not include legacy runtime traffic", kind)
	}
}

func isLegacyRuntimeKind(kind int) bool {
	return (kind >= 5941 && kind <= 7999) ||
		(kind >= 31100 && kind <= 31399) ||
		(kind >= 31900 && kind <= 32099) ||
		(kind >= 38390 && kind <= 38499)
}

func TestSubscriberBuildSubscriptionFiltersOmitsLegacyProductionKinds(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5101, 5961, 5963, 5978, 5979, 5981, 5991, 31100}),
		WithAuthorizedAuthorScopes(AuthorizedAuthorScopes{
			Default:       []string{"default-a", "shared"},
			Adoption:      []string{"adoption-a", "shared"},
			DirectRuntime: []string{"runtime-a", "shared"},
		}),
	)

	filters, err := sub.buildSubscriptionFilters()
	require.NoError(t, err)
	require.Len(t, filters, 1)
	require.Equal(t, []gonostr.Kind{canonicalKind(5101)}, filters[0].filter.Kinds)
	require.Empty(t, filters[0].filter.Authors, "legacy production kinds must be omitted from runtime subscriptions")
	require.Zero(t, filters[0].filter.Since, "a REQ's since comes from its relay's cursor, not from the filter")
	require.Zero(t, filters[0].filter.Limit)
}

func TestSubscriberBuildSubscriptionFiltersScopesConfigDesiredAuthors(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{kinds.ConfigACLList, kinds.ConfigPolicy, kinds.CASControlState}),
		WithAuthorizedAuthors([]string{strings.Repeat("a", 64)}),
	)

	filters, err := sub.buildSubscriptionFilters()
	require.NoError(t, err)
	require.Len(t, filters, 2)
	require.ElementsMatch(t, []gonostr.Kind{canonicalKind(kinds.CASControlState)}, filters[0].filter.Kinds)
	require.Empty(t, filters[0].filter.Authors)
	require.ElementsMatch(t, []gonostr.Kind{canonicalKind(kinds.ConfigACLList), canonicalKind(kinds.ConfigPolicy)}, filters[1].filter.Kinds)
	require.Len(t, filters[1].filter.Authors, 1)
}

// Replaceable/addressable kinds and regular kinds catch up differently (full
// NIP-77 reconcile vs cursor + until paging), so they never share a filter,
// and each filter has its own cursor hash.
func TestSubscriberBuildSubscriptionFiltersSplitsPersistentAndRegularKinds(t *testing.T) {
	sub := NewSubscriber(nil, nil, zap.NewNop())

	filters, err := sub.buildSubscriptionFilters()
	require.NoError(t, err)
	hashes := make(map[string]struct{})
	var persistent, regular []gonostr.Kind
	for _, filter := range filters {
		hashes[filter.hash] = struct{}{}
		for _, kind := range filter.filter.Kinds {
			require.Equal(t, filter.persistent, kind.IsReplaceable() || kind.IsAddressable(), "kind %d is in the wrong class of filter", kind)
			if filter.persistent {
				persistent = append(persistent, kind)
			} else {
				regular = append(regular, kind)
			}
		}
	}
	require.Len(t, hashes, len(filters))
	require.ElementsMatch(t, []gonostr.Kind{KindCASAudit, KindHiveCIWorkflowRun, KindHiveCIWorkflowResult, KindLoomJobResult, KindLoomJobCancellation}, regular)
	require.Contains(t, persistent, gonostr.Kind(kinds.ConfigACLList))
	require.Contains(t, persistent, gonostr.Kind(KindLoomWorkerAdvertisement))
}

func TestSubscriberBuildSubscriptionFiltersOmitsLegacyCommandKinds(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithKinds([]int{5961, 38390, 38394, 31980, 31986, 31100, 5101}),
		WithAuthorizedAuthors([]string{"operator-a", "operator-b"}),
	)

	filters, err := sub.buildSubscriptionFilters()
	require.NoError(t, err)
	require.Len(t, filters, 1)

	open := filters[0].filter
	require.Equal(t, []gonostr.Kind{canonicalKind(5101)}, open.Kinds)
	require.Empty(t, open.Authors)
}

func TestSubscriberHandleEventRetriesPersistenceAfterTransientRecordError(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	ev := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	repo.failRecordID = eventIDHex(ev)

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, eventIDHex(ev))
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, ev)
	require.Empty(t, handled)
	require.Nil(t, repo.latest)

	sub.handleEvent(ctx, ev)
	require.Equal(t, []string{eventIDHex(ev)}, handled)
}

func TestSubscriberHandleEventInjectsCanonicalMLReadModelAndMarksEOSECaughtUp(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	ev := signedTestEvent(t, KindCASControlState, time.Unix(105, 0).UTC())
	ev.Tags = gonostr.Tags{{"d", "ml:endpoint-state:qwen:prod"}, {"domain", "ml"}, {"schema", "bahia.ml.endpoint-state.v1"}, {"entity", "endpoint-state"}, {"endpoint", "endpoint:qwen:prod"}, {"status", "healthy"}}
	ev.Content = `{"endpoint":"endpoint:qwen:prod","status":"healthy"}`
	require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, tagValue(ev.Tags, "d"))
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, ev)
	require.Equal(t, []string{"ml:endpoint-state:qwen:prod"}, handled)
	require.Equal(t, 1, repo.inserted)
	require.False(t, sub.IsCaughtUp())

	progress := map[string]relayProgress{"wss://a.example": {}, "wss://b.example": {}}
	progress["wss://a.example"] = relayProgress{caughtUp: true}
	sub.updateCaughtUp(progress)
	require.False(t, sub.IsCaughtUp(), "a relay still catching up holds back caught-up")
	progress["wss://b.example"] = relayProgress{failed: true}
	sub.updateCaughtUp(progress)
	require.True(t, sub.IsCaughtUp(), "caught up once every relay synced or failed its first attempt")
}

func TestSubscriberHandleEventInvokesHandlersOnlyForNewlyPersistedEvents(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	persistedEvent := signedTestEvent(t, 5101, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, &repository.NostrEventRecord{
		ID:        eventIDHex(persistedEvent),
		Kind:      eventKindInt(persistedEvent),
		PubKey:    eventPubKeyHex(persistedEvent),
		Content:   persistedEvent.Content,
		Tags:      json.RawMessage("[]"),
		Sig:       eventSignatureHex(persistedEvent),
		CreatedAt: persistedEvent.CreatedAt.Time(),
	})
	require.NoError(t, err)

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, eventIDHex(ev))
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, persistedEvent)
	require.Empty(t, handled, "persisted overlap duplicate must not re-run handlers")

	newEvent := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	sub.handleEvent(ctx, newEvent)
	require.Equal(t, []string{eventIDHex(newEvent)}, handled)
}

// A self-published observable is persisted by the publisher before its relay
// attempt, so its relay echo is always an already-persisted duplicate. Side-effect
// handlers must stay gated, but idempotent projections still have to see it or
// Bahia's own fleet-health observables are invisible to its own telemetry.
func TestSubscriberObserversSeeSelfPublishedEchoWhileHandlersStayGated(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	echo := signedTestEvent(t, KindCASControlState, time.Unix(100, 0).UTC())
	inserted, err := repo.Record(ctx, &repository.NostrEventRecord{
		ID:           eventIDHex(echo),
		Kind:         eventKindInt(echo),
		PubKey:       eventPubKeyHex(echo),
		Content:      echo.Content,
		Tags:         json.RawMessage("[]"),
		Sig:          eventSignatureHex(echo),
		CreatedAt:    echo.CreatedAt.Time(),
		PublishState: repository.NostrPublishStatePending,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	var handled, observed []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) { handled = append(handled, eventIDHex(ev)) }),
		WithObserver(func(_ context.Context, ev *gonostr.Event) { observed = append(observed, eventIDHex(ev)) }),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, echo)
	require.Empty(t, handled, "a persisted echo must not re-run side-effect handlers")
	require.Equal(t, []string{eventIDHex(echo)}, observed, "a persisted echo must reach idempotent observers")

	fresh := signedTestEvent(t, KindCASControlState, time.Unix(105, 0).UTC())
	sub.handleEvent(ctx, fresh)
	require.Equal(t, []string{eventIDHex(fresh)}, handled)
	require.Equal(t, []string{eventIDHex(echo), eventIDHex(fresh)}, observed)

	invalid := *signedTestEvent(t, KindCASControlState, time.Unix(106, 0).UTC())
	invalid.ID = gonostr.ID{}
	sub.handleEvent(ctx, &invalid)

	unpersisted := signedTestEvent(t, KindCASControlState, time.Unix(107, 0).UTC())
	repo.failRecordID = eventIDHex(unpersisted)
	sub.handleEvent(ctx, unpersisted)
	require.Equal(t, []string{eventIDHex(echo), eventIDHex(fresh)}, observed,
		"observers only see events that validated and are durably persisted")
}

func TestSubscriberHandleEventDropsLegacyProductionKindBeforePersistence(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	legacy := signedTestEvent(t, 5961, time.Unix(105, 0).UTC())

	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, eventIDHex(ev))
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, legacy)
	require.Empty(t, handled)
	require.Equal(t, 0, repo.inserted)
}

func TestSubscriberHandleEventDropsInvalidBeforePersistenceAndDispatch(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	now := time.Unix(200, 0).UTC()
	valid := signedTestEvent(t, 5101, time.Unix(105, 0).UTC())
	invalid := *valid
	invalid.ID = gonostr.ID{}
	var handled []string
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithHandler(func(_ context.Context, ev *gonostr.Event) {
			handled = append(handled, eventIDHex(ev))
		}),
		withClock(func() time.Time { return now }),
	)

	sub.handleEvent(ctx, &invalid)
	require.Empty(t, handled)
	require.Equal(t, 0, repo.inserted)
}

// C-14: the local store is the idempotency gate, so a restarted subscriber
// with no Postgres (here no audit repository at all) does not re-run handlers
// for events it already handled, while observers still see the redelivery.
func TestSubscriberHandleEventDedupsAgainstTheLocalStoreAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "daemon.bolt")
	now := time.Now().UTC()
	ev := signedTestEvent(t, 5101, now.Add(-time.Minute))

	var handled, observed int
	newSub := func(store *localstore.Store) *Subscriber {
		return NewSubscriber(nil, nil, zap.NewNop(),
			WithLocalStore(store),
			WithHandler(func(context.Context, *gonostr.Event) { handled++ }),
			WithObserver(func(context.Context, *gonostr.Event) { observed++ }),
		)
	}
	first := openTestLocalStore(t, path)
	require.Equal(t, ingestNew, newSub(first).handleEvent(ctx, ev))
	require.NoError(t, first.Close())

	restarted := openTestLocalStore(t, path)
	require.Equal(t, ingestDuplicate, newSub(restarted).handleEvent(ctx, ev))
	require.Equal(t, 1, handled)
	require.Equal(t, 2, observed)
}

// With an audit repository configured, a failed audit write undoes the local
// store write, so the redelivery is still new and its handlers run exactly
// once when the write succeeds.
func TestSubscriberHandleEventRollsBackTheLocalStoreWhenTheAuditWriteFails(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	store := openTestLocalStore(t, "")
	ev := signedTestEvent(t, 5101, time.Now().UTC().Add(-time.Minute))
	repo.failRecordID = eventIDHex(ev)
	var handled int
	sub := NewSubscriber(nil, repo, zap.NewNop(),
		WithLocalStore(store),
		WithHandler(func(context.Context, *gonostr.Event) { handled++ }),
	)

	require.Equal(t, ingestFailed, sub.handleEvent(ctx, ev))
	require.False(t, localStoreHas(store, ev.ID))
	require.Equal(t, ingestNew, sub.handleEvent(ctx, ev))
	require.Equal(t, ingestDuplicate, sub.handleEvent(ctx, ev))
	require.Equal(t, 1, handled)
}

// The daemon's own events reach observers but never side-effect handlers,
// whether or not the publisher pre-recorded them in Postgres.
func TestSubscriberHandleEventKeepsSelfAuthoredEventsFromHandlers(t *testing.T) {
	ctx := context.Background()
	store := openTestLocalStore(t, "")
	own := signedTestEvent(t, KindCASControlState, time.Now().UTC().Add(-time.Minute))
	var handled, observed int
	sub := NewSubscriber(nil, nil, zap.NewNop(),
		WithLocalStore(store),
		WithSelfAuthors(eventPubKeyHex(own)),
		WithHandler(func(context.Context, *gonostr.Event) { handled++ }),
		WithObserver(func(context.Context, *gonostr.Event) { observed++ }),
	)

	require.Equal(t, ingestNew, sub.handleEvent(ctx, own))
	require.Zero(t, handled)
	require.Equal(t, 1, observed)
}
