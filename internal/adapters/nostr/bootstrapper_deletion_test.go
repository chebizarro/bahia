package nostr

import (
	"context"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// deletionTestCache is a projection cache that keeps entities by d tag and
// honours NIP-09: a deletion request removes the entities its `e` tags name,
// when they have the same author. It resolves decoded projections back to
// their signed events through signed.
type deletionTestCache struct {
	mu       sync.Mutex
	signed   map[string]gonostr.Event
	entities map[string]string // d tag -> source event id
	applied  chan string
}

func newDeletionTestCache(events ...gonostr.Event) *deletionTestCache {
	cache := &deletionTestCache{signed: make(map[string]gonostr.Event), entities: make(map[string]string), applied: make(chan string, 64)}
	cache.know(events...)
	return cache
}

func (c *deletionTestCache) know(events ...gonostr.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range events {
		c.signed[ev.ID.Hex()] = ev
	}
}

func (c *deletionTestCache) Apply(_ context.Context, decoded *DecodedProjectionEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { c.applied <- decoded.SourceID }()
	source, ok := c.signed[decoded.SourceID]
	if !ok {
		return nil
	}
	if source.Kind != gonostr.KindDeletion {
		c.entities[source.Tags.GetD()] = decoded.SourceID
		return nil
	}
	for _, tag := range source.Tags {
		if len(tag) < 2 || tag[0] != "e" {
			continue
		}
		for d, id := range c.entities {
			if target, known := c.signed[id]; known && id == tag[1] && target.PubKey == source.PubKey {
				delete(c.entities, d)
			}
		}
	}
	return nil
}

func (c *deletionTestCache) has(d string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entities[d]
	return ok
}

// deletionTestCatalog replays one projected state kind and the production
// catalog's deletion group, with decoders that carry the source id.
func deletionTestCatalog(t *testing.T) *KindCatalog {
	t.Helper()
	var deletion ReplayGroup
	for _, group := range NewKindCatalog().Groups {
		if group.Name == "deletion_live" {
			deletion = group
		}
	}
	require.Equal(t, []int{int(gonostr.KindDeletion)}, deletion.Kinds, "the production catalog replays NIP-09 deletions")
	require.Equal(t, ReplayAuthorsControlPlane, deletion.Authors, "only trusted control-plane authors' deletions are replayed")
	catalog := &KindCatalog{Version: "deletion-test", decoders: make(map[int]DecodeFunc), Groups: []ReplayGroup{
		{Name: "state", Kinds: []int{testKindTier1Snapshot}, Tier: 1, Snapshot: true, Required: true, Authors: ReplayAuthorsProjection},
		deletion,
	}}
	for _, kind := range []int{testKindTier1Snapshot, int(gonostr.KindDeletion)} {
		catalog.decoders[kind] = func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
			return &DecodedProjectionEvent{Kind: eventKindInt(ev), DTag: eventIDHex(ev), Timestamp: ev.CreatedAt.Time(), SourceID: eventIDHex(ev), Family: "test"}, nil
		}
	}
	return catalog
}

func deletionRequest(t *testing.T, privateKeyHex string, createdAt gonostr.Timestamp, targets ...gonostr.Event) gonostr.Event {
	t.Helper()
	ev := gonostr.Event{Kind: gonostr.KindDeletion, CreatedAt: createdAt}
	for _, target := range targets {
		ev.Tags = append(ev.Tags, gonostr.Tag{"e", target.ID.Hex()})
	}
	require.NoError(t, signEventWithPrivateKeyHex(&ev, privateKeyHex))
	return ev
}

func deletionTestBootstrapper(t *testing.T, pool *RelayPool, cache BootstrapCacheApplier) *Bootstrapper {
	t.Helper()
	service := bootstrapTestPubkey(t, testNostrPrivateKey)
	return NewBootstrapper(pool, deletionTestCatalog(t), nil, cache, zap.NewNop(), BootstrapConfig{
		RequestedTier:       1,
		SnapshotTimeout:     time.Minute,
		CatchupTimeout:      time.Minute,
		ProjectionAuthors:   []string{service},
		ControlPlaneAuthors: []string{service},
	})
}

// A deletion request stored on the relay is replayed during backfill, whatever
// its age, and removes the projected entity; a stranger's request for the
// same kind is neither requested nor applied.
func TestBootstrapperBackfillAppliesNIP09DeletionsFromControlPlaneAuthors(t *testing.T) {
	now := gonostr.Now()
	deleted := signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier1Snapshot, "deleted", now-90*24*3600)
	kept := signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier1Snapshot, "kept", now-90*24*3600)
	deletion := deletionRequest(t, testNostrPrivateKey, now-80*24*3600, deleted)
	strangerDeletion := deletionRequest(t, bootstrapTestStrangerKey, now-60, kept)
	relay := &bootstrapFakeRelay{url: "wss://relay.example", store: []gonostr.Event{deleted, kept, deletion, strangerDeletion}}
	cache := newDeletionTestCache(deleted, kept, deletion, strangerDeletion)

	bootstrapper := deletionTestBootstrapper(t, newBootstrapFakeRelayPool(t, relay), cache)
	require.NoError(t, bootstrapper.attemptBootstrap(context.Background()))

	require.False(t, cache.has("deleted"), "the backfilled deletion removes the entity")
	require.True(t, cache.has("kept"), "a stranger's deletion is not applied")
	var deletionReq *gonostr.Filter
	for _, filter := range relay.recordedFilters() {
		if len(filter.Kinds) == 1 && filter.Kinds[0] == gonostr.KindDeletion {
			deletionReq = &filter
		}
	}
	require.NotNil(t, deletionReq, "kind 5 is requested during backfill")
	require.Zero(t, deletionReq.Since, "deletions are replayed in full, like the state they delete")
	require.Equal(t, []gonostr.PubKey{deletion.PubKey}, deletionReq.Authors)
}

// A deletion request published after bootstrap reaches the projection cache
// through the subscriber's live REQ (Bootstrapper.ApplyDeletion as observer).
func TestSubscriberAppliesLiveNIP09DeletionsToTheProjectionCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	now := gonostr.Now()
	entity := signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier1Snapshot, "entity", now-600)
	relay.add(t, entity)
	cache := newDeletionTestCache(entity)
	pool := newSyncTestPool(relay)
	pool.Connect(ctx)
	bootstrapper := deletionTestBootstrapper(t, pool, cache)
	require.NoError(t, bootstrapper.attemptBootstrap(ctx))
	require.True(t, cache.has("entity"))

	service := bootstrapTestPubkey(t, testNostrPrivateKey)
	run := startSyncRun(t, pool, openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds(nil),
		WithDeletionAuthors([]string{service}),
		WithObserver(bootstrapper.ApplyDeletion),
	)
	run.waitLive(t, ctx, relay.url, 1)

	stranger := deletionRequest(t, bootstrapTestStrangerKey, gonostr.Now(), entity)
	relay.publishLive(t, stranger)
	deletion := deletionRequest(t, testNostrPrivateKey, gonostr.Now(), entity)
	cache.know(stranger, deletion)
	relay.publishLive(t, deletion)
	run.waitApplied(t, ctx, deletion.ID)

	require.False(t, cache.has("entity"), "the live deletion removes the entity")
	require.NotContains(t, drainAppliedIDs(cache), stranger.ID.Hex(), "a stranger's deletion is not delivered or applied")
}

func drainAppliedIDs(cache *deletionTestCache) []string {
	var ids []string
	for {
		select {
		case id := <-cache.applied:
			ids = append(ids, id)
		default:
			return ids
		}
	}
}
