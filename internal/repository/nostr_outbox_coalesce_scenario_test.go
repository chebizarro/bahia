package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// runCoalesceScenario exercises NIP-01 replacement semantics against any
// outbox repository so the in-memory and Postgres implementations agree.
func runCoalesceScenario(t *testing.T, repo NostrEventOutboxRepository) {
	t.Helper()
	ctx := t.Context()
	base := time.Unix(1_700_000_000, 0).UTC()
	record := func(id string, kind int, pubkey string, createdAt int, tags [][]string, state string) {
		t.Helper()
		if tags == nil {
			tags = [][]string{}
		}
		raw, err := json.Marshal(tags)
		require.NoError(t, err)
		_, err = repo.Record(ctx, &NostrEventRecord{
			ID: id, Kind: kind, PubKey: pubkey, Content: "", Tags: raw, Sig: "sig",
			CreatedAt: base.Add(time.Duration(createdAt) * time.Second), ReceivedAt: base,
			EntityType: "test", PublishState: state,
		})
		require.NoError(t, err)
	}
	pending, published, inbound := NostrPublishStatePending, NostrPublishStatePublished, NostrPublishStateNotApplicable

	// Replaceable (kind, pubkey): the newer revision wins.
	record("a1", 10002, "pkA", 100, nil, pending)
	record("a2", 10002, "pkA", 200, nil, pending)
	// Same timestamp: the lowest event ID wins.
	record("t9", 0, "pkA", 300, nil, pending)
	record("t1", 0, "pkA", 300, nil, pending)
	// Addressable (kind, pubkey, first d): distinct d tags never coalesce;
	// the first d tag decides the address.
	record("d1", 30315, "pkA", 100, [][]string{{"d", "x"}}, pending)
	record("d2", 30315, "pkA", 200, [][]string{{"d", "y"}}, pending)
	record("d4", 30315, "pkA", 120, [][]string{{"t", "z"}, {"d", "x"}, {"d", "y"}}, pending)
	// Only pending revisions compete: newer published or inbound revisions are
	// history, not outbox work, and are never scanned.
	record("o1", 10002, "pkC", 100, nil, pending)
	record("o2", 10002, "pkC", 200, nil, published)
	record("o3", 30315, "pkC", 100, [][]string{{"d", "x"}}, pending)
	record("o4", 30315, "pkC", 200, [][]string{{"d", "x"}}, inbound)
	// A missing d tag and an empty d tag are the same address.
	record("e1", 30000, "pkB", 100, nil, pending)
	record("e2", 30000, "pkB", 200, [][]string{{"d", ""}}, pending)
	// Other pubkeys, regular kinds, and ephemeral kinds are never coalesced.
	record("b1", 10002, "pkB", 50, nil, pending)
	record("g1", 1, "pkA", 100, nil, pending)
	record("g2", 1, "pkA", 200, nil, pending)
	record("p1", 25910, "pkA", 100, nil, pending)
	record("p2", 25910, "pkA", 200, nil, pending)

	superseded, err := repo.CoalesceSupersededUnpublished(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(4), superseded)
	again, err := repo.CoalesceSupersededUnpublished(ctx)
	require.NoError(t, err)
	require.Zero(t, again, "coalescing is idempotent")

	for _, id := range []string{"a1", "t9", "d1", "e1"} {
		rec, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, NostrPublishStateSuperseded, rec.PublishState, id)
		require.Contains(t, rec.LastPublishError, "superseded", id)
	}
	for id, state := range map[string]string{
		"a2": pending, "t1": pending, "d2": pending, "d4": pending, "e2": pending,
		"o1": pending, "o2": published, "o3": pending, "o4": inbound,
		"b1": pending, "g1": pending, "g2": pending, "p1": pending, "p2": pending,
	} {
		rec, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, state, rec.PublishState, id)
	}

	// A superseded row is terminal: a late failure cannot resurrect it.
	require.NoError(t, repo.RecordPublishFailure(ctx, "a1", "late failure"))
	rec, err := repo.GetByID(ctx, "a1")
	require.NoError(t, err)
	require.Equal(t, NostrPublishStateSuperseded, rec.PublishState)
}

func TestInMemoryNostrOutboxCoalescesSupersededRevisions(t *testing.T) {
	runCoalesceScenario(t, NewInMemoryNostrEventRepository())
}

func TestNostrReplacementClasses(t *testing.T) {
	for _, kind := range []int{0, 3, 10000, 10002, 19999} {
		require.True(t, IsNostrReplaceableKind(kind), kind)
		require.False(t, IsNostrAddressableKind(kind), kind)
	}
	for _, kind := range []int{30000, 30315, 39999} {
		require.True(t, IsNostrAddressableKind(kind), kind)
	}
	for _, kind := range []int{1, 5, 1059, 4903, 20000, 25910, 29999, 40000} {
		require.False(t, IsNostrReplaceableKind(kind), kind)
		require.False(t, IsNostrAddressableKind(kind), kind)
	}
}
