package relaysidecar

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func openTestEventStore(t *testing.T, dir string) *eventStore {
	t.Helper()
	store, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func signedStoreEvent(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, createdAt nostr.Timestamp, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags, Content: content}
	require.NoError(t, event.Sign(sk))
	return event
}

func storeIDs(t *testing.T, store *eventStore, filter nostr.Filter) []nostr.ID {
	t.Helper()
	var ids []nostr.ID
	for event := range store.Query(t.Context(), filter, 0) {
		ids = append(ids, event.ID)
	}
	return ids
}

// coordinateFilter selects the versions stored under one coordinate whose d
// the eventstore indexes. Plain replaceable kinds ignore d.
func coordinateFilter(kind nostr.Kind, author nostr.PubKey, d string) nostr.Filter {
	filter := nostr.Filter{Kinds: []nostr.Kind{kind}, Authors: []nostr.PubKey{author}}
	if kind.IsAddressable() {
		filter.Tags = nostr.TagMap{"d": []string{d}}
	}
	return filter
}

func saveOrReplace(t *testing.T, store *eventStore, event nostr.Event) {
	t.Helper()
	if event.Kind.IsReplaceable() || event.Kind.IsAddressable() {
		require.NoError(t, store.Replace(t.Context(), event))
		return
	}
	require.NoError(t, store.Save(t.Context(), event))
}

// TestEventStoreTagIndexedQueries covers C-20: #p, #e, #d, #a and #t filters
// are answered from the eventstore's tag indexes, combined with kinds,
// authors, since/until and limit, newest first. COUNT uses the same indexes.
func TestEventStoreTagIndexedQueries(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	alice, bob := nostr.Generate(), nostr.Generate()
	recipient := nostr.Generate().Public().Hex()
	base := nostr.Timestamp(1_790_000_000)

	request := signedStoreEvent(t, alice, kinds.ContextVMGiftWrap, base, nostr.Tags{{"p", recipient}}, "wrap")
	reply := signedStoreEvent(t, bob, kinds.ContextVMGiftWrap, base+1, nostr.Tags{{"p", recipient}, {"e", request.ID.Hex()}}, "reply")
	other := signedStoreEvent(t, bob, kinds.ContextVMGiftWrap, base+2, nostr.Tags{{"p", nostr.Generate().Public().Hex()}}, "other")
	state := signedStoreEvent(t, alice, 30900, base+3, nostr.Tags{{"d", "service:web"}, {"t", "service-state"}}, `{}`)
	otherState := signedStoreEvent(t, alice, 30900, base+4, nostr.Tags{{"d", "service:api"}, {"t", "service-state"}}, `{}`)
	address := fmt.Sprintf("30900:%s:service:web", alice.Public().Hex())
	audit := signedStoreEvent(t, alice, 4903, base+5, nostr.Tags{{"a", address}, {"t", "audit"}}, "audit")
	for _, event := range []nostr.Event{request, reply, other, state, otherState, audit} {
		saveOrReplace(t, store, event)
	}

	for name, tc := range map[string]struct {
		filter nostr.Filter
		want   []nostr.ID
	}{
		"#p ContextVM inbox": {nostr.Filter{Kinds: []nostr.Kind{kinds.ContextVMGiftWrap}, Tags: nostr.TagMap{"p": {recipient}}}, []nostr.ID{reply.ID, request.ID}},
		"#e reply lookup":    {nostr.Filter{Tags: nostr.TagMap{"e": {request.ID.Hex()}}}, []nostr.ID{reply.ID}},
		"#d coordinate":      {nostr.Filter{Kinds: []nostr.Kind{30900}, Authors: []nostr.PubKey{alice.Public()}, Tags: nostr.TagMap{"d": {"service:web"}}}, []nostr.ID{state.ID}},
		"#a references":      {nostr.Filter{Tags: nostr.TagMap{"a": {address}}}, []nostr.ID{audit.ID}},
		"#t topic":           {nostr.Filter{Kinds: []nostr.Kind{30900}, Tags: nostr.TagMap{"t": {"service-state"}}}, []nostr.ID{otherState.ID, state.ID}},
		"#p with author":     {nostr.Filter{Authors: []nostr.PubKey{bob.Public()}, Tags: nostr.TagMap{"p": {recipient}}}, []nostr.ID{reply.ID}},
		"#p with since":      {nostr.Filter{Since: base + 1, Tags: nostr.TagMap{"p": {recipient}}}, []nostr.ID{reply.ID}},
		"#p with until":      {nostr.Filter{Until: base, Tags: nostr.TagMap{"p": {recipient}}}, []nostr.ID{request.ID}},
		"#p with limit":      {nostr.Filter{Limit: 1, Tags: nostr.TagMap{"p": {recipient}}}, []nostr.ID{reply.ID}},
		"#t any of":          {nostr.Filter{Tags: nostr.TagMap{"t": {"audit", "missing"}}}, []nostr.ID{audit.ID}},
		"ids with kinds":     {nostr.Filter{IDs: []nostr.ID{request.ID, state.ID}, Kinds: []nostr.Kind{30900}}, []nostr.ID{state.ID}},
		"empty kinds":        {nostr.Filter{Kinds: []nostr.Kind{}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, storeIDs(t, store, tc.filter))
			if tc.filter.Limit == 0 {
				count, err := store.Count(t.Context(), tc.filter)
				require.NoError(t, err)
				require.EqualValues(t, len(tc.want), count)
			}
		})
	}
}

// TestEventStoreRetentionByClass covers C-19: request/transport kinds are
// swept after the request retention (paging through more than one sweep
// batch); regular facts are durable by default and swept only under a
// configured cap; replaceable, addressable and kind-5 events are never
// age-swept; NIP-40 expiration is honoured.
func TestEventStoreRetentionByClass(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	store.backend().DB.NoSync = true        // thousands of single-event commits
	now := time.Unix(int64(nostr.Now()), 0) // Query hides by the real clock
	ts := func(age time.Duration) nostr.Timestamp { return nostr.Timestamp(now.Add(-age).Unix()) }
	sk := nostr.Generate()

	var oldRequests []nostr.Event
	for i := range 2*sweepBatchSize + 7 {
		event := signedStoreEvent(t, sk, kinds.ContextVMGiftWrap, ts(25*time.Hour+time.Duration(i)*time.Second), nil, "old wrap")
		oldRequests = append(oldRequests, event)
		saveOrReplace(t, store, event)
	}
	// A full sweep page of latest-wins events, all sharing one timestamp, sits
	// between the cutoff and the regular events: the regular-cap sweep must
	// page past it.
	for i := range sweepBatchSize + 1 {
		saveOrReplace(t, store, signedStoreEvent(t, sk, 30315, ts(40*24*time.Hour), nostr.Tags{{"d", "status-" + strconv.Itoa(i)}}, "addressable"))
	}
	keep := map[string]nostr.Event{
		"fresh wrap":        signedStoreEvent(t, sk, kinds.ContextVMGiftWrap, ts(23*time.Hour), nil, "fresh wrap"),
		"old replaceable":   signedStoreEvent(t, sk, 10002, ts(400*24*time.Hour), nil, "relay list"),
		"old addressable":   signedStoreEvent(t, sk, 30900, ts(400*24*time.Hour), nostr.Tags{{"d", "state"}}, "state"),
		"old tombstone":     signedStoreEvent(t, sk, nostr.KindDeletion, ts(400*24*time.Hour), nostr.Tags{{"e", nostr.Generate().Public().Hex()}}, ""),
		"fresh audit":       signedStoreEvent(t, sk, 4903, ts(24*time.Hour), nil, "fresh audit"),
		"not yet expiring":  signedStoreEvent(t, sk, 4903, ts(time.Hour), nostr.Tags{{"expiration", strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}, ""),
		"old durable audit": signedStoreEvent(t, sk, 4903, ts(50*24*time.Hour), nil, "old audit"),
	}
	expiredEvent := signedStoreEvent(t, sk, 4903, ts(2*time.Hour), nostr.Tags{{"expiration", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)}}, "")
	for _, event := range keep {
		saveOrReplace(t, store, event)
	}
	saveOrReplace(t, store, expiredEvent)

	cfg := config.Defaults().Nostr.Sidecar
	result, err := store.SweepRetention(t.Context(), now, newRetentionPolicy(cfg))
	require.NoError(t, err)
	require.Equal(t, sweepResult{Expired: 1, Request: int64(len(oldRequests))}, result)
	for name, event := range keep {
		require.Equal(t, []nostr.ID{event.ID}, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{event.ID}}), name)
	}
	require.Empty(t, storeIDs(t, store, nostr.Filter{Kinds: []nostr.Kind{kinds.ContextVMGiftWrap}, Until: ts(24 * time.Hour)}))

	// With a 30-day cap only the old regular audit goes; the latest-wins
	// events and the tombstone stay whatever their age.
	cfg.EventRetention = 30 * 24 * time.Hour
	result, err = store.SweepRetention(t.Context(), now, newRetentionPolicy(cfg))
	require.NoError(t, err)
	require.Equal(t, sweepResult{Regular: 1}, result)
	require.Empty(t, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{keep["old durable audit"].ID}}))
	delete(keep, "old durable audit")
	for name, event := range keep {
		require.Equal(t, []nostr.ID{event.ID}, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{event.ID}}), name)
	}
	count, err := store.Count(t.Context(), nostr.Filter{Kinds: []nostr.Kind{30315}})
	require.NoError(t, err)
	require.EqualValues(t, sweepBatchSize+1, count)
}

func TestEventStoreHidesExpiredEventsBeforeTheSweep(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	sk := nostr.Generate()
	past := signedStoreEvent(t, sk, 1, nostr.Now()-10, nostr.Tags{{"expiration", strconv.FormatInt(int64(nostr.Now()-1), 10)}}, "")
	future := signedStoreEvent(t, sk, 1, nostr.Now()-10, nostr.Tags{{"expiration", strconv.FormatInt(int64(nostr.Now()+3600), 10)}}, "")
	saveOrReplace(t, store, past)
	saveOrReplace(t, store, future)
	require.Equal(t, []nostr.ID{future.ID}, storeIDs(t, store, nostr.Filter{Kinds: []nostr.Kind{1}}))
}

func TestRetentionPolicyClassesAndNIP11(t *testing.T) {
	cfg := config.Defaults().Nostr.Sidecar
	// An unvalidated config must still never age-sweep latest-wins or
	// tombstone kinds.
	cfg.RequestRetentionKinds = append(cfg.RequestRetentionKinds, 30900, 10002, 5, 70000)
	policy := newRetentionPolicy(cfg)
	for kind, want := range map[nostr.Kind]retentionClass{
		25910: retentionEphemeral, 21059: retentionEphemeral, 1059: retentionRequest,
		0: retentionLatestWins, 3: retentionLatestWins, 10002: retentionLatestWins, 30900: retentionLatestWins,
		5: retentionTombstone, 1: retentionRegular, 4903: retentionRegular,
	} {
		require.Equal(t, want, policy.classOf(kind), "kind %d", kind)
	}
	require.Equal(t, []nostr.Kind{1059}, policy.storedRequestKinds())
	docs := policy.nip11()
	require.Len(t, docs, 2, "no regular cap by default")
	require.Equal(t, int64(24*60*60), docs[0].Time)
	require.Equal(t, [][]int{{1059, 1059}}, docs[0].Kinds)
	require.Zero(t, docs[1].Time)
	require.Contains(t, docs[1].Kinds, []int{5, 5})
	require.Contains(t, docs[1].Kinds, []int{30000, 39999})
	cfg.EventRetention = 90 * 24 * time.Hour
	docs = newRetentionPolicy(cfg).nip11()
	require.Len(t, docs, 3)
	require.Equal(t, int64(90*24*60*60), docs[2].Time)
	require.Nil(t, docs[2].Kinds)
}

// TestEventStoreNIP09 covers deletion semantics at the store: `e` references
// delete only the requester's events; `a` references delete every version of
// the requester's coordinate up to the request's created_at (addressable and
// plain replaceable); deleted events and older versions are never re-accepted,
// including when the request arrives first; deleting a deletion does nothing.
func TestEventStoreNIP09(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	alice, mallory := nostr.Generate(), nostr.Generate()
	base := nostr.Timestamp(1_790_000_000)

	note := signedStoreEvent(t, alice, 1, base, nil, "note")
	foreign := signedStoreEvent(t, mallory, 1, base, nil, "mallory's note")
	saveOrReplace(t, store, note)
	saveOrReplace(t, store, foreign)
	deletion := signedStoreEvent(t, alice, nostr.KindDeletion, base+10, nostr.Tags{{"e", note.ID.Hex()}, {"e", foreign.ID.Hex()}}, "")
	require.NoError(t, store.Save(t.Context(), deletion))
	require.Empty(t, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{note.ID}}))
	require.Equal(t, []nostr.ID{foreign.ID}, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{foreign.ID}}), "another author's event survives")
	require.ErrorIs(t, store.Save(t.Context(), note), errEventDeleted)
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, mallory, 1, base+1, nil, "unrelated")))

	// Addressable: all versions up to the request go; older versions stay out;
	// a newer version is accepted.
	v1 := signedStoreEvent(t, alice, 30078, base, nostr.Tags{{"d", "app"}}, "v1")
	v2 := signedStoreEvent(t, alice, 30078, base+5, nostr.Tags{{"d", "app"}}, "v2")
	saveOrReplace(t, store, v1)
	saveOrReplace(t, store, v2)
	address := fmt.Sprintf("30078:%s:app", alice.Public().Hex())
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+10, nostr.Tags{{"a", address}}, "")))
	coordinate := coordinateFilter(30078, alice.Public(), "app")
	require.Empty(t, storeIDs(t, store, coordinate))
	require.ErrorIs(t, store.Replace(t.Context(), v1), errEventDeleted)
	require.ErrorIs(t, store.Replace(t.Context(), signedStoreEvent(t, alice, 30078, base+10, nostr.Tags{{"d", "app"}}, "same second")), errEventDeleted)
	v3 := signedStoreEvent(t, alice, 30078, base+11, nostr.Tags{{"d", "app"}}, "v3")
	require.NoError(t, store.Replace(t.Context(), v3))
	require.Equal(t, []nostr.ID{v3.ID}, storeIDs(t, store, coordinate))

	// Plain replaceable, addressed as "<kind>:<pubkey>:".
	relays := signedStoreEvent(t, alice, 10002, base, nil, "")
	saveOrReplace(t, store, relays)
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+1, nostr.Tags{{"a", fmt.Sprintf("10002:%s:", alice.Public().Hex())}}, "")))
	require.Empty(t, storeIDs(t, store, nostr.Filter{Kinds: []nostr.Kind{10002}, Authors: []nostr.PubKey{alice.Public()}}))

	// Another author's `a` reference deletes nothing.
	malloryState := signedStoreEvent(t, mallory, 30078, base, nostr.Tags{{"d", "app"}}, "")
	saveOrReplace(t, store, malloryState)
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+20, nostr.Tags{{"a", fmt.Sprintf("30078:%s:app", mallory.Public().Hex())}}, "")))
	require.Equal(t, []nostr.ID{malloryState.ID}, storeIDs(t, store, coordinateFilter(30078, mallory.Public(), "app")))

	// The request arrives before the event it deletes.
	late := signedStoreEvent(t, alice, 1, base+30, nil, "late")
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+31, nostr.Tags{{"e", late.ID.Hex()}}, "")))
	require.ErrorIs(t, store.Save(t.Context(), late), errEventDeleted)

	// Deleting a deletion request has no effect.
	undo := signedStoreEvent(t, alice, nostr.KindDeletion, base+40, nostr.Tags{{"e", deletion.ID.Hex()}}, "")
	require.NoError(t, store.Save(t.Context(), undo))
	require.Equal(t, []nostr.ID{deletion.ID}, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{deletion.ID}}))
	require.ErrorIs(t, store.Save(t.Context(), note), errEventDeleted)
}

func TestEventStoreRefusesEventsBeyondCodecLimits(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	sk := nostr.Generate()
	for name, event := range map[string]nostr.Event{
		"content": signedStoreEvent(t, sk, kinds.ContextVMGiftWrap, 1_790_000_000, nil, strings.Repeat("x", 65536)),
		"tag section": signedStoreEvent(t, sk, 3, 1_790_000_000, func() nostr.Tags {
			var tags nostr.Tags
			for range 1000 {
				tags = append(tags, nostr.Tag{"p", nostr.Generate().Public().Hex()})
			}
			return tags
		}(), ""),
		"tag items": signedStoreEvent(t, sk, 1, 1_790_000_000, nostr.Tags{slices.Repeat(nostr.Tag{"x"}, 256)}, ""),
	} {
		err := store.Replace(t.Context(), event)
		require.Error(t, err, name)
		require.True(t, strings.HasPrefix(err.Error(), "invalid: "), "%s: %v", name, err)
		require.Empty(t, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{event.ID}}), name)
	}
	fits := signedStoreEvent(t, sk, kinds.ContextVMGiftWrap, 1_790_000_000, nil, strings.Repeat("x", 65535))
	require.NoError(t, store.Save(t.Context(), fits))
}

// TestEventStoreHandlesShareOneDatabase covers the SIGHUP path: cmd/relay
// opens the replacement runtime's store before it closes the active one. bbolt
// locks its file, so a second open must share the first, and the database must
// outlive the handle closed first.
func TestEventStoreHandlesShareOneDatabase(t *testing.T) {
	dir := t.TempDir()
	first, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	second, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	event := signedStoreEvent(t, nostr.Generate(), 1, 1_790_000_000, nil, "")
	require.NoError(t, first.Save(t.Context(), event))
	require.NoError(t, first.Close())
	require.NoError(t, first.Close(), "closing a handle twice is a no-op")
	require.Equal(t, []nostr.ID{event.ID}, storeIDs(t, second, nostr.Filter{IDs: []nostr.ID{event.ID}}))
	require.ErrorIs(t, second.Save(t.Context(), event), eventstore.ErrDupEvent)
	require.NoError(t, second.Close())
	require.Error(t, second.ping(), "the last handle closes the database")

	reopened := openTestEventStore(t, dir)
	require.Equal(t, []nostr.ID{event.ID}, storeIDs(t, reopened, nostr.Filter{IDs: []nostr.ID{event.ID}}))
}
