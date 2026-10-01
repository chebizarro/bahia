package nostrutil

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

const (
	lifecycleAuthorKey = "1111111111111111111111111111111111111111111111111111111111111111"
	lifecycleOtherKey  = "2222222222222222222222222222222222222222222222222222222222222222"
)

var lifecycleNow = time.Unix(1_800_000_000, 0).UTC()

func lifecycleEvent(t *testing.T, key string, kind canonicalnostr.Kind, createdAt time.Time, content string, tags ...canonicalnostr.Tag) *canonicalnostr.Event {
	t.Helper()
	ev := &canonicalnostr.Event{Kind: kind, CreatedAt: canonicalnostr.Timestamp(createdAt.Unix()), Content: content, Tags: canonicalnostr.Tags(tags)}
	require.NoError(t, SignEventWithHexKey(ev, key))
	return ev
}

// sameSecondPair returns two versions of one coordinate with equal
// created_at, ordered (lower id, higher id).
func sameSecondPair(t *testing.T, at time.Time) (*canonicalnostr.Event, *canonicalnostr.Event) {
	t.Helper()
	a := lifecycleEvent(t, lifecycleAuthorKey, 30078, at, "a", canonicalnostr.Tag{"d", "cfg"})
	b := lifecycleEvent(t, lifecycleAuthorKey, 30078, at, "b", canonicalnostr.Tag{"d", "cfg"})
	if a.ID.Hex() > b.ID.Hex() {
		a, b = b, a
	}
	return a, b
}

func TestVersionSupersedesLatestThenLowestID(t *testing.T) {
	older := Version{CreatedAt: 10, ID: "00"}
	newer := Version{CreatedAt: 11, ID: "ff"}
	require.True(t, newer.Supersedes(older))
	require.False(t, older.Supersedes(newer))
	low := Version{CreatedAt: 10, ID: "0a"}
	high := Version{CreatedAt: 10, ID: "0b"}
	require.True(t, low.Supersedes(high))
	require.False(t, high.Supersedes(low))
	require.False(t, low.Supersedes(low))
}

func TestLifecycleTieBreakIsArrivalOrderIndependent(t *testing.T) {
	low, high := sameSecondPair(t, lifecycleNow.Add(-time.Minute))

	first := NewLifecycle()
	require.Equal(t, OutcomeAccept, first.Observe(high, lifecycleNow).Outcome)
	decision := first.Observe(low, lifecycleNow)
	require.Equal(t, OutcomeAccept, decision.Outcome)
	require.NotNil(t, decision.Replaced)
	require.Equal(t, high.ID, decision.Replaced.ID)

	second := NewLifecycle()
	require.Equal(t, OutcomeAccept, second.Observe(low, lifecycleNow).Outcome)
	require.Equal(t, OutcomeSuperseded, second.Observe(high, lifecycleNow).Outcome)
	require.Equal(t, OutcomeDuplicate, second.Observe(low, lifecycleNow).Outcome)
}

func TestLifecycleOlderVersionNeverReplacesNewer(t *testing.T) {
	l := NewLifecycle()
	newer := lifecycleEvent(t, lifecycleAuthorKey, 10002, lifecycleNow.Add(-time.Minute), "new")
	older := lifecycleEvent(t, lifecycleAuthorKey, 10002, lifecycleNow.Add(-time.Hour), "old")
	require.Equal(t, OutcomeAccept, l.Observe(newer, lifecycleNow).Outcome)
	require.Equal(t, OutcomeSuperseded, l.Observe(older, lifecycleNow).Outcome)
}

func deletionEvent(t *testing.T, key string, at time.Time, tags ...canonicalnostr.Tag) *canonicalnostr.Event {
	t.Helper()
	return lifecycleEvent(t, key, canonicalnostr.KindDeletion, at, "", tags...)
}

func TestLifecycleEDeletionRemovesLiveEventAndBlocksResurrection(t *testing.T) {
	l := NewLifecycle()
	target := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Hour), "x", canonicalnostr.Tag{"d", "cfg"})
	require.Equal(t, OutcomeAccept, l.Observe(target, lifecycleNow).Outcome)

	decision := l.Observe(deletionEvent(t, lifecycleAuthorKey, lifecycleNow.Add(-time.Minute), canonicalnostr.Tag{"e", target.ID.Hex()}), lifecycleNow)
	require.Equal(t, OutcomeDeletion, decision.Outcome)
	require.Len(t, decision.Removed, 1)
	require.Equal(t, target.ID, decision.Removed[0].ID)

	require.Equal(t, OutcomeDeleted, l.Observe(target, lifecycleNow).Outcome, "a deleted event must not resurrect")
}

func TestLifecycleEDeletionBeforeTargetArrives(t *testing.T) {
	l := NewLifecycle()
	target := lifecycleEvent(t, lifecycleAuthorKey, 1, lifecycleNow.Add(-time.Hour), "note")
	require.Equal(t, OutcomeDeletion, l.Observe(deletionEvent(t, lifecycleAuthorKey, lifecycleNow, canonicalnostr.Tag{"e", target.ID.Hex()}), lifecycleNow).Outcome)
	require.Equal(t, OutcomeDeleted, l.Observe(target, lifecycleNow).Outcome)
}

func TestLifecycleDeletionByAnotherAuthorIsIgnored(t *testing.T) {
	l := NewLifecycle()
	target := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Hour), "x", canonicalnostr.Tag{"d", "cfg"})
	address, _ := AddressOf(target)
	require.Equal(t, OutcomeAccept, l.Observe(target, lifecycleNow).Outcome)
	decision := l.Observe(deletionEvent(t, lifecycleOtherKey, lifecycleNow, canonicalnostr.Tag{"e", target.ID.Hex()}, canonicalnostr.Tag{"a", address.String()}), lifecycleNow)
	require.Equal(t, OutcomeDeletion, decision.Outcome)
	require.Empty(t, decision.Removed)
	require.Equal(t, OutcomeDuplicate, l.Observe(target, lifecycleNow).Outcome)
}

func TestLifecycleADeletionRemovesVersionsUpToItsCreatedAt(t *testing.T) {
	l := NewLifecycle()
	v1 := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-2*time.Hour), "v1", canonicalnostr.Tag{"d", "cfg"})
	v2 := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Hour), "v2", canonicalnostr.Tag{"d", "cfg"})
	address, ok := AddressOf(v2)
	require.True(t, ok)
	require.Equal(t, OutcomeAccept, l.Observe(v2, lifecycleNow).Outcome)

	deletion := deletionEvent(t, lifecycleAuthorKey, lifecycleNow.Add(-30*time.Minute), canonicalnostr.Tag{"a", address.String()})
	decision := l.Observe(deletion, lifecycleNow)
	require.Equal(t, OutcomeDeletion, decision.Outcome)
	require.Len(t, decision.Removed, 1)
	require.Equal(t, v2.ID, decision.Removed[0].ID)

	require.Equal(t, OutcomeDeleted, l.Observe(v1, lifecycleNow).Outcome, "an older version must not resurrect")
	require.Equal(t, OutcomeDeleted, l.Observe(v2, lifecycleNow).Outcome)
	v3 := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Minute), "v3", canonicalnostr.Tag{"d", "cfg"})
	require.Equal(t, OutcomeAccept, l.Observe(v3, lifecycleNow).Outcome, "a version newer than the deletion is new state")
}

func TestLifecycleADeletionBeforeTargetArrives(t *testing.T) {
	l := NewLifecycle()
	target := lifecycleEvent(t, lifecycleAuthorKey, 10002, lifecycleNow.Add(-time.Hour), "relays")
	address, _ := AddressOf(target)
	require.Equal(t, "10002:"+target.PubKey.Hex()+":", address.String())
	require.Equal(t, OutcomeDeletion, l.Observe(deletionEvent(t, lifecycleAuthorKey, lifecycleNow, canonicalnostr.Tag{"a", address.String()}), lifecycleNow).Outcome)
	require.Equal(t, OutcomeDeleted, l.Observe(target, lifecycleNow).Outcome)
}

func expiringEvent(t *testing.T, createdAt, expiresAt time.Time) *canonicalnostr.Event {
	t.Helper()
	return lifecycleEvent(t, lifecycleAuthorKey, 30078, createdAt, "x", canonicalnostr.Tag{"d", "cfg"}, canonicalnostr.Tag{"expiration", strconv.FormatInt(expiresAt.Unix(), 10)})
}

func TestLifecycleIgnoresExpiredAndExpiresLiveEvents(t *testing.T) {
	l := NewLifecycle()
	require.Equal(t, OutcomeExpired, l.Observe(expiringEvent(t, lifecycleNow.Add(-time.Hour), lifecycleNow), lifecycleNow).Outcome)

	live := expiringEvent(t, lifecycleNow.Add(-time.Minute), lifecycleNow.Add(time.Minute))
	require.Equal(t, OutcomeAccept, l.Observe(live, lifecycleNow).Outcome)
	at, ok := l.NextExpiry()
	require.True(t, ok)
	require.Equal(t, lifecycleNow.Add(time.Minute).Unix(), at.Unix())

	require.Empty(t, l.Expire(lifecycleNow))
	expired := l.Expire(lifecycleNow.Add(time.Minute))
	require.Len(t, expired, 1)
	require.Equal(t, live.ID, expired[0].ID)
	_, ok = l.NextExpiry()
	require.False(t, ok)
}

func TestLifecycleReplacedVersionDoesNotExpireTheNewOne(t *testing.T) {
	l := NewLifecycle()
	first := expiringEvent(t, lifecycleNow.Add(-time.Hour), lifecycleNow.Add(time.Minute))
	second := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Minute), "y", canonicalnostr.Tag{"d", "cfg"})
	require.Equal(t, OutcomeAccept, l.Observe(first, lifecycleNow).Outcome)
	require.Equal(t, OutcomeAccept, l.Observe(second, lifecycleNow).Outcome)
	require.Empty(t, l.Expire(lifecycleNow.Add(time.Hour)))
}

func TestLifecycleRunExpiryDropsAtExpirationWithoutPolling(t *testing.T) {
	l := NewLifecycle()
	armed := make(chan time.Duration, 4)
	fire := make(chan time.Time)
	l.newTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		armed <- d
		return fire, func() bool { return true }
	}
	var clock atomic.Int64
	clock.Store(lifecycleNow.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	dropped := make(chan []Entry, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.RunExpiry(ctx, now, func(entries []Entry) { dropped <- entries })
	}()

	live := expiringEvent(t, lifecycleNow.Add(-time.Minute), lifecycleNow.Add(90*time.Second))
	require.Equal(t, OutcomeAccept, l.Observe(live, lifecycleNow).Outcome)
	require.Equal(t, 90*time.Second, <-armed)

	clock.Store(lifecycleNow.Add(90 * time.Second).Unix())
	fire <- lifecycleNow.Add(90 * time.Second)
	entries := <-dropped
	require.Len(t, entries, 1)
	require.Equal(t, live.ID, entries[0].ID)

	cancel()
	<-done
}

func TestParseDeletionKeepsOnlyOwnCoordinates(t *testing.T) {
	own := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow, "x", canonicalnostr.Tag{"d", "cfg"})
	other := lifecycleEvent(t, lifecycleOtherKey, 30078, lifecycleNow, "x", canonicalnostr.Tag{"d", "cfg"})
	ownAddress, _ := AddressOf(own)
	otherAddress, _ := AddressOf(other)
	deletion, ok := ParseDeletion(deletionEvent(t, lifecycleAuthorKey, lifecycleNow,
		canonicalnostr.Tag{"a", ownAddress.String()},
		canonicalnostr.Tag{"a", otherAddress.String()},
		canonicalnostr.Tag{"a", "1:" + own.PubKey.Hex() + ":"},
		canonicalnostr.Tag{"e", own.ID.Hex()},
	))
	require.True(t, ok)
	require.Equal(t, []Address{ownAddress}, deletion.Addresses)
	require.Equal(t, []canonicalnostr.ID{own.ID}, deletion.IDs)
}

func TestLifecycleReleaseLetsARedeliveryApplyAgain(t *testing.T) {
	l := NewLifecycle()
	ev := lifecycleEvent(t, lifecycleAuthorKey, 30078, lifecycleNow.Add(-time.Minute), "x", canonicalnostr.Tag{"d", "cfg"})
	require.Equal(t, OutcomeAccept, l.Observe(ev, lifecycleNow).Outcome)
	l.Release(ev.ID)
	require.Equal(t, OutcomeAccept, l.Observe(ev, lifecycleNow).Outcome)
}
