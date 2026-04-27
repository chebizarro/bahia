package nostr

import (
	"testing"
)

func TestEventDeduplicator_IsDuplicate(t *testing.T) {
	d := NewEventDeduplicator(5)

	// First time seeing an event - not a duplicate.
	if d.IsDuplicate("event1") {
		t.Error("event1 should not be duplicate on first check")
	}

	// Second time - should be duplicate.
	if !d.IsDuplicate("event1") {
		t.Error("event1 should be duplicate on second check")
	}

	// Different event - not a duplicate.
	if d.IsDuplicate("event2") {
		t.Error("event2 should not be duplicate on first check")
	}

	if d.Size() != 2 {
		t.Errorf("expected size 2, got %d", d.Size())
	}
}

func TestEventDeduplicator_Eviction(t *testing.T) {
	d := NewEventDeduplicator(3)

	// Add 3 events.
	d.IsDuplicate("event1")
	d.IsDuplicate("event2")
	d.IsDuplicate("event3")

	if d.Size() != 3 {
		t.Errorf("expected size 3, got %d", d.Size())
	}

	// Add 4th event - should evict oldest (event1).
	d.IsDuplicate("event4")

	if d.Size() != 3 {
		t.Errorf("expected size 3 after eviction, got %d", d.Size())
	}

	// event2, event3, event4 should still be tracked (before checking event1).
	if !d.IsDuplicate("event2") {
		t.Error("event2 should still be tracked")
	}
	if !d.IsDuplicate("event3") {
		t.Error("event3 should still be tracked")
	}
	if !d.IsDuplicate("event4") {
		t.Error("event4 should still be tracked")
	}

	// Now check event1 - it should have been evicted.
	// Note: IsDuplicate will re-add it if not found, so we need a separate check.
	// Since we don't have a "Contains" method, we test eviction indirectly:
	// After adding event5, event2 should be evicted (event1 was already evicted).
	d.IsDuplicate("event5")
	
	// Size should still be 3.
	if d.Size() != 3 {
		t.Errorf("expected size 3, got %d", d.Size())
	}
}

func TestEventDeduplicator_LRUBehavior(t *testing.T) {
	d := NewEventDeduplicator(3)

	// Add 3 events.
	d.IsDuplicate("event1")
	d.IsDuplicate("event2")
	d.IsDuplicate("event3")

	// Access event1 again (moves it to front).
	d.IsDuplicate("event1")

	// Add event4 - should evict event2 (now the oldest).
	d.IsDuplicate("event4")

	// event2 should be evicted.
	if d.IsDuplicate("event2") {
		t.Error("event2 should have been evicted")
	}

	// event1 should still be tracked (was accessed recently).
	if !d.IsDuplicate("event1") {
		t.Error("event1 should still be tracked after recent access")
	}
}

func TestEventDeduplicator_MarkSeen(t *testing.T) {
	d := NewEventDeduplicator(5)

	// Mark as seen without checking.
	d.MarkSeen("event1")
	d.MarkSeen("event2")

	// Should be duplicates now.
	if !d.IsDuplicate("event1") {
		t.Error("event1 should be duplicate after MarkSeen")
	}
	if !d.IsDuplicate("event2") {
		t.Error("event2 should be duplicate after MarkSeen")
	}

	if d.Size() != 2 {
		t.Errorf("expected size 2, got %d", d.Size())
	}
}

func TestEventDeduplicator_Clear(t *testing.T) {
	d := NewEventDeduplicator(5)

	d.IsDuplicate("event1")
	d.IsDuplicate("event2")

	d.Clear()

	if d.Size() != 0 {
		t.Errorf("expected size 0 after clear, got %d", d.Size())
	}

	// Previously seen events should not be duplicates after clear.
	if d.IsDuplicate("event1") {
		t.Error("event1 should not be duplicate after clear")
	}
}

func TestEventDeduplicator_DefaultCapacity(t *testing.T) {
	// Zero capacity should use default.
	d := NewEventDeduplicator(0)

	// Add an event - should work with default capacity.
	if d.IsDuplicate("event1") {
		t.Error("event1 should not be duplicate")
	}

	if d.Size() != 1 {
		t.Errorf("expected size 1, got %d", d.Size())
	}
}
