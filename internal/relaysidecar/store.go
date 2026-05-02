package relaysidecar

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"sync"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
)

// memoryStore is the phase-1 sidecar store: intentionally in-memory and
// rebuildable. Later projector work can replace this with a durable eventstore.
type memoryStore struct {
	mu     sync.RWMutex
	events map[nostr.ID]nostr.Event
	latest map[string]nostr.ID
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		events: make(map[nostr.ID]nostr.Event),
		latest: make(map[string]nostr.ID),
	}
}

func (s *memoryStore) Save(ctx context.Context, event nostr.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.ID]; exists {
		return eventstore.ErrDupEvent
	}
	s.events[event.ID] = event
	return nil
}

func (s *memoryStore) Replace(ctx context.Context, event nostr.Event) error {
	key := replaceableKey(event)
	if key == "" {
		return s.Save(ctx, event)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.ID]; exists {
		return eventstore.ErrDupEvent
	}
	if previousID, ok := s.latest[key]; ok {
		previous := s.events[previousID]
		if previous.CreatedAt >= event.CreatedAt {
			return eventstore.ErrDupEvent
		}
		delete(s.events, previousID)
	}
	s.latest[key] = event.ID
	s.events[event.ID] = event
	return nil
}

func (s *memoryStore) Delete(ctx context.Context, id nostr.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event, ok := s.events[id]; ok {
		delete(s.latest, replaceableKey(event))
	}
	delete(s.events, id)
	return nil
}

func (s *memoryStore) Count(ctx context.Context, filter nostr.Filter) uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count uint32
	for _, event := range s.events {
		if filter.Matches(event) {
			count++
		}
	}
	return count
}

func (s *memoryStore) Query(ctx context.Context, filter nostr.Filter, maxLimit int) iter.Seq[nostr.Event] {
	s.mu.RLock()
	events := make([]nostr.Event, 0, len(s.events))
	for _, event := range s.events {
		if filter.Matches(event) {
			events = append(events, event)
		}
	}
	s.mu.RUnlock()

	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})

	limit := maxLimit
	if filter.Limit > 0 && filter.Limit < limit {
		limit = filter.Limit
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}

	return func(yield func(nostr.Event) bool) {
		for i := 0; i < limit; i++ {
			if !yield(events[i]) {
				return
			}
		}
	}
}

func replaceableKey(event nostr.Event) string {
	if event.Kind.IsReplaceable() {
		return fmt.Sprintf("%d:%s", event.Kind, event.PubKey.Hex())
	}
	if event.Kind.IsAddressable() {
		return fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey.Hex(), event.Tags.GetD())
	}
	return ""
}
