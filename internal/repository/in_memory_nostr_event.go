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
	cursors map[string]NostrMigrationCursor
}

var _ NostrEventOutboxRepository = (*InMemoryNostrEventRepository)(nil)

// NewInMemoryNostrEventRepository creates an empty in-memory Nostr event repository.
func NewInMemoryNostrEventRepository() *InMemoryNostrEventRepository {
	return &InMemoryNostrEventRepository{records: make(map[string]NostrEventRecord), cursors: make(map[string]NostrMigrationCursor)}
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
	if stored.PublishState == "" {
		stored.PublishState = NostrPublishStateNotApplicable
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

// ListUnpublished returns the oldest pending outbound events first.
func (r *InMemoryNostrEventRepository) ListUnpublished(_ context.Context, limit int) ([]NostrEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if rec.PublishState == NostrPublishStatePending {
			records = append(records, cloneNostrEventRecord(&rec))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].ReceivedAt.Equal(records[j].ReceivedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].ReceivedAt.Before(records[j].ReceivedAt)
	})
	return limitNostrEventRecords(records, limit), nil
}

// CountUnpublished returns the current in-memory publish outbox depth.
func (r *InMemoryNostrEventRepository) CountUnpublished(_ context.Context) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var count int64
	for _, rec := range r.records {
		if rec.PublishState == NostrPublishStatePending {
			count++
		}
	}
	return count, nil
}

// MarkPublished records a successful relay acceptance (including duplicate OK).
func (r *InMemoryNostrEventRepository) MarkPublished(_ context.Context, id string, publishedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok {
		return nil
	}
	rec.PublishState = NostrPublishStatePublished
	rec.PublishAttempts++
	rec.LastPublishError = ""
	rec.PublishedAt = &publishedAt
	r.records[id] = rec
	return nil
}

// RecordPublishFailure retains the event as pending and records retry diagnostics.
func (r *InMemoryNostrEventRepository) RecordPublishFailure(_ context.Context, id, publishError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok {
		return nil
	}
	rec.PublishState = NostrPublishStatePending
	rec.PublishAttempts++
	rec.LastPublishError = publishError
	r.records[id] = rec
	return nil
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

// ListByKinds returns the oldest events for any of kinds so migrations process deterministically.
func (r *InMemoryNostrEventRepository) ListByKinds(_ context.Context, kinds []int, limit int) ([]NostrEventRecord, error) {
	return r.ListByKindsPage(context.Background(), kinds, nil, limit)
}

func (r *InMemoryNostrEventRepository) ListByKindsPage(_ context.Context, kinds []int, after *NostrMigrationCursor, limit int) ([]NostrEventRecord, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	kindSet := intSet(kinds)
	r.mu.RLock()
	defer r.mu.RUnlock()

	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if _, ok := kindSet[rec.Kind]; ok {
			if after != nil && (rec.CreatedAt.Before(after.CreatedAt) || (rec.CreatedAt.Equal(after.CreatedAt) && rec.ID <= after.EventID)) {
				continue
			}
			records = append(records, cloneNostrEventRecord(&rec))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return limitNostrEventRecords(records, limit), nil
}

func (r *InMemoryNostrEventRepository) GetMigrationCursor(_ context.Context, name string) (*NostrMigrationCursor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cursor, ok := r.cursors[name]
	if !ok {
		return nil, nil
	}
	copy := cursor
	return &copy, nil
}

func (r *InMemoryNostrEventRepository) SaveMigrationCursor(_ context.Context, cursor NostrMigrationCursor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.cursors[cursor.Name]
	if !ok || existing.CreatedAt.Before(cursor.CreatedAt) || (existing.CreatedAt.Equal(cursor.CreatedAt) && existing.EventID < cursor.EventID) {
		r.cursors[cursor.Name] = cursor
	}
	return nil
}

// FindByTag returns events containing tagName=tagValue, optionally restricted by kind.
func (r *InMemoryNostrEventRepository) FindByTag(_ context.Context, tagName, tagValue string, kinds []int, limit int) ([]NostrEventRecord, error) {
	if tagName == "" || tagValue == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	kindSet := intSet(kinds)
	r.mu.RLock()
	defer r.mu.RUnlock()

	records := make([]NostrEventRecord, 0)
	for _, rec := range r.records {
		if len(kindSet) > 0 {
			if _, ok := kindSet[rec.Kind]; !ok {
				continue
			}
		}
		if recordHasTag(rec.Tags, tagName, tagValue) {
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
	return recordHasTag(raw, "d", dTag)
}

func recordHasTag(raw json.RawMessage, tagName, tagValue string) bool {
	if tagName == "" || tagValue == "" {
		return false
	}
	var tags [][]string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return false
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == tagName && tag[1] == tagValue {
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
	if rec.PublishedAt != nil {
		publishedAt := *rec.PublishedAt
		cloned.PublishedAt = &publishedAt
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
