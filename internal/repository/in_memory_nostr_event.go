package repository

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// InMemoryNostrEventRepository is a mutex-protected in-memory implementation of NostrEventRepository.
type InMemoryNostrEventRepository struct {
	mu      sync.RWMutex
	records map[string]NostrEventRecord
}

var _ NostrEventRepository = (*InMemoryNostrEventRepository)(nil)

// NewInMemoryNostrEventRepository creates an empty in-memory Nostr event repository.
func NewInMemoryNostrEventRepository() *InMemoryNostrEventRepository {
	return &InMemoryNostrEventRepository{records: make(map[string]NostrEventRecord)}
}

// Record stores rec by ID. Duplicate IDs are accepted idempotently and reported as inserted=false.
func (r *InMemoryNostrEventRepository) Record(_ context.Context, rec *NostrEventRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.records[rec.ID]; exists {
		return false, nil
	}

	stored := cloneNostrEventRecord(rec)
	if stored.ReceivedAt.IsZero() {
		stored.ReceivedAt = time.Now().UTC()
	}
	if stored.Tags == nil {
		stored.Tags = json.RawMessage("[]")
	}
	r.records[stored.ID] = stored
	return true, nil
}

// GetByID retrieves a Nostr event by ID.
func (r *InMemoryNostrEventRepository) GetByID(_ context.Context, id string) (*NostrEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.records[id]
	if !ok {
		return nil, nil
	}
	return cloneNostrEventRecordPtr(&rec), nil
}

// FindByID retrieves a Nostr event by ID.
func (r *InMemoryNostrEventRepository) FindByID(ctx context.Context, id string) (*NostrEventRecord, error) {
	return r.GetByID(ctx, id)
}

// FindSince returns events created after since, filtered by kinds when provided.
func (r *InMemoryNostrEventRepository) FindSince(_ context.Context, since time.Time, kinds []int) ([]NostrEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	kindSet := intSet(kinds)
	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if !rec.CreatedAt.After(since) {
			continue
		}
		if len(kindSet) > 0 {
			if _, ok := kindSet[rec.Kind]; !ok {
				continue
			}
		}
		records = append(records, cloneNostrEventRecord(&rec))
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

// FindLatestByKindPubkeyDTag returns the newest event with the same kind, pubkey, and Nostr d tag.
func (r *InMemoryNostrEventRepository) FindLatestByKindPubkeyDTag(_ context.Context, kind int, pubkey, dTag, excludeID string) (*NostrEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var newest *NostrEventRecord
	for _, rec := range r.records {
		if rec.ID == excludeID || rec.Kind != kind || rec.PubKey != pubkey || !recordHasDTag(rec.Tags, dTag) {
			continue
		}
		candidate := cloneNostrEventRecord(&rec)
		if newest == nil || candidate.CreatedAt.After(newest.CreatedAt) {
			newest = &candidate
		}
	}
	return newest, nil
}

// ListByKind returns the most recent events of a given kind.
func (r *InMemoryNostrEventRepository) ListByKind(_ context.Context, kind int, limit int) ([]NostrEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}
	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if rec.Kind == kind {
			records = append(records, cloneNostrEventRecord(&rec))
		}
	}
	sortNostrEventRecordsNewestFirst(records)
	return limitNostrEventRecords(records, limit), nil
}

// ListByEntity returns the most recent events for a given entity.
func (r *InMemoryNostrEventRepository) ListByEntity(_ context.Context, entityType string, entityID uuid.UUID, limit int) ([]NostrEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}
	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if rec.EntityType == entityType && rec.EntityID != nil && *rec.EntityID == entityID {
			records = append(records, cloneNostrEventRecord(&rec))
		}
	}
	sortNostrEventRecordsNewestFirst(records)
	return limitNostrEventRecords(records, limit), nil
}

// LatestCreatedAtForKinds returns the newest created_at cursor for any of kinds.
func (r *InMemoryNostrEventRepository) LatestCreatedAtForKinds(_ context.Context, kinds []int) (*time.Time, error) {
	if len(kinds) == 0 {
		return nil, nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	kindSet := intSet(kinds)
	var latest *time.Time
	for _, rec := range r.records {
		if _, ok := kindSet[rec.Kind]; !ok {
			continue
		}
		if latest == nil || rec.CreatedAt.After(*latest) {
			createdAt := rec.CreatedAt
			latest = &createdAt
		}
	}
	return latest, nil
}

// LatestCreatedAtForKindsAndAuthors returns the newest cursor for events matching kinds and authors.
func (r *InMemoryNostrEventRepository) LatestCreatedAtForKindsAndAuthors(_ context.Context, kinds []int, authors []string) (*time.Time, error) {
	if len(kinds) == 0 || len(authors) == 0 {
		return nil, nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	kindSet := intSet(kinds)
	authorSet := stringSet(authors)
	var latest *time.Time
	for _, rec := range r.records {
		if _, ok := kindSet[rec.Kind]; !ok {
			continue
		}
		if _, ok := authorSet[rec.PubKey]; !ok {
			continue
		}
		if latest == nil || rec.CreatedAt.After(*latest) {
			createdAt := rec.CreatedAt
			latest = &createdAt
		}
	}
	return latest, nil
}

func recordHasDTag(raw json.RawMessage, dTag string) bool {
	if dTag == "" {
		return false
	}
	var tags [][]string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return false
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "d" && tag[1] == dTag {
			return true
		}
	}
	return false
}

func cloneNostrEventRecordPtr(rec *NostrEventRecord) *NostrEventRecord {
	if rec == nil {
		return nil
	}
	cloned := cloneNostrEventRecord(rec)
	return &cloned
}

func cloneNostrEventRecord(rec *NostrEventRecord) NostrEventRecord {
	cloned := *rec
	if rec.Tags != nil {
		cloned.Tags = append(json.RawMessage(nil), rec.Tags...)
	}
	if rec.EntityID != nil {
		entityID := *rec.EntityID
		cloned.EntityID = &entityID
	}
	return cloned
}

func intSet(values []int) map[int]struct{} {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func sortNostrEventRecordsNewestFirst(records []NostrEventRecord) {
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
}

func limitNostrEventRecords(records []NostrEventRecord, limit int) []NostrEventRecord {
	if len(records) <= limit {
		return records
	}
	return records[:limit]
}
