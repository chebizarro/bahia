// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"container/list"
	"sync"
)

// EventDeduplicator tracks seen event IDs to prevent duplicate processing.
// It uses an LRU cache with a bounded size to prevent unbounded memory growth.
type EventDeduplicator struct {
	mu       sync.Mutex
	seen     map[string]*list.Element
	order    *list.List
	capacity int
}

// NewEventDeduplicator creates a new deduplicator with the given capacity.
// When capacity is reached, the oldest entries are evicted.
func NewEventDeduplicator(capacity int) *EventDeduplicator {
	if capacity <= 0 {
		capacity = 10000 // default: track last 10k events
	}
	return &EventDeduplicator{
		seen:     make(map[string]*list.Element),
		order:    list.New(),
		capacity: capacity,
	}
}

// IsDuplicate returns true if the event ID has been seen before.
// If not seen, it marks the ID as seen and returns false.
// This is an atomic check-and-mark operation.
func (d *EventDeduplicator) IsDuplicate(eventID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if already seen.
	if elem, exists := d.seen[eventID]; exists {
		// Move to front (most recently seen).
		d.order.MoveToFront(elem)
		return true
	}

	// Not seen - add to cache.
	elem := d.order.PushFront(eventID)
	d.seen[eventID] = elem

	// Evict oldest if over capacity.
	for d.order.Len() > d.capacity {
		oldest := d.order.Back()
		if oldest != nil {
			oldID := oldest.Value.(string)
			delete(d.seen, oldID)
			d.order.Remove(oldest)
		}
	}

	return false
}

// MarkSeen marks an event ID as seen without checking for duplicates.
// Useful for pre-populating from database on startup.
func (d *EventDeduplicator) MarkSeen(eventID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.seen[eventID]; exists {
		return
	}

	elem := d.order.PushFront(eventID)
	d.seen[eventID] = elem

	// Evict oldest if over capacity.
	for d.order.Len() > d.capacity {
		oldest := d.order.Back()
		if oldest != nil {
			oldID := oldest.Value.(string)
			delete(d.seen, oldID)
			d.order.Remove(oldest)
		}
	}
}

// Size returns the current number of tracked event IDs.
func (d *EventDeduplicator) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.order.Len()
}

// Clear removes all tracked event IDs.
func (d *EventDeduplicator) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = make(map[string]*list.Element)
	d.order.Init()
}
