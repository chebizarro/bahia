package negentropy

import (
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

// TestReleaseFreesBlockedEmit: a session consumer that stopped reading (its
// session ended) must not wedge the relay read loop — Release frees an emit
// blocked on an unread channel, the aborted Reconcile reports it, and a
// buffered emit is unaffected.
func TestReleaseFreesBlockedEmit(t *testing.T) {
	n := New(nil, 60_000, true, false)
	n.Haves = make(chan nostr.ID) // unbuffered, no reader: emit blocks
	emitted := make(chan bool, 1)
	go func() { emitted <- n.emit(n.Haves, nostr.ID{}) }()

	n.Release()
	select {
	case ok := <-emitted:
		if ok {
			t.Fatal("emit reported success on a released session")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Release did not free the blocked emit")
	}
	n.Release() // idempotent

	// A released session still aborts a Reconcile emit deterministically,
	// and an unreleased buffered channel accepts without a reader.
	fresh := New(nil, 60_000, true, false)
	if !fresh.emit(fresh.Haves, nostr.ID{}) {
		t.Fatal("buffered emit should succeed")
	}
	fresh.Release()
	if fresh.emit(make(chan nostr.ID), nostr.ID{}) {
		t.Fatal("emit after Release must report the release")
	}
}
