package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/repository"
)

// LocalEventRepository serves the nostr_events reads and writes of the
// daemon's PostgreSQL-backed components from the local event store
// It replaces the unbounded in-memory
// nostr_events fallback of PostgreSQL-less mode: the store is persistent,
// collapses replaceable and addressable events to their latest version, and
// prunes regular events, so it stays bounded and survives restarts.
//
// It stores signed events, not rows: entity labels are not kept (ListByEntity
// is always empty), and the only publish state a record carries is
// NostrPublishStateFailed, on an event whose delivery the outbox abandoned
// (localstore.Undelivered): readers see the daemon's committed
// state and that relays do not hold it, and the Projector's dedupe does not
// treat it as delivered. Two kinds of "pending" row are honoured:
//   - a row for a PostgreSQL-drained publish target, which a producer records
//     so that it gets delivered, is handed to the publisher outbox for that
//     target (see WithPendingAdmission);
//   - an archive row of the local outbox (LocalOutboxArchiveTarget) is only
//     stored: the producer publishes it through a Publisher itself.
//
// Authored returns a view limited to some authors, which the Projector uses
// as its memory of what the daemon published.
type LocalEventRepository struct {
	store   *localstore.Store
	authors []nostr.PubKey
	admit   PendingAdmission
}

// PendingAdmission hands an event recorded as pending delivery to the outbox
// that delivers target.
type PendingAdmission func(ctx context.Context, ev nostr.Event, target, entityType string, entityID *uuid.UUID) error

var _ repository.NostrEventRepository = (*LocalEventRepository)(nil)

// NewLocalEventRepository returns a repository over store. admit may be nil,
// in which case recording a row as pending delivery fails.
func NewLocalEventRepository(store *localstore.Store, admit PendingAdmission) *LocalEventRepository {
	return &LocalEventRepository{store: store, admit: admit}
}

// Authored returns a view of the repository whose reads only return events
// by these authors (hex pubkeys; invalid ones are ignored).
func (r *LocalEventRepository) Authored(pubkeys ...string) *LocalEventRepository {
	view := *r
	view.authors = nil
	for _, pubkey := range pubkeys {
		if parsed, err := nostr.PubKeyFromHex(strings.TrimSpace(pubkey)); err == nil {
			view.authors = append(view.authors, parsed)
		}
	}
	return &view
}

// Record stores the signed event in rec and reports whether the store did not
// hold it yet (see the type comment for pending rows).
func (r *LocalEventRepository) Record(ctx context.Context, rec *repository.NostrEventRecord) (bool, error) {
	if rec == nil {
		return false, errors.New("nostr event record is required")
	}
	ev, err := eventFromNostrRecord(*rec)
	if err != nil {
		return false, fmt.Errorf("record nostr event %s locally: %w", rec.ID, err)
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return false, fmt.Errorf("record nostr event %s locally: invalid id or signature", rec.ID)
	}
	inserted, err := r.store.SaveEvent(ev)
	if err != nil {
		return false, err
	}
	if rec.PublishState == repository.NostrPublishStatePending && !repository.IsLocalOutboxArchiveTarget(rec.PublishTarget) {
		if r.admit == nil {
			return inserted, fmt.Errorf("record nostr event %s as pending delivery: no outbox for target %q", rec.ID, rec.PublishTarget)
		}
		if err := r.admit(ctx, ev, rec.PublishTarget, rec.EntityType, rec.EntityID); err != nil {
			return inserted, fmt.Errorf("record nostr event %s as pending delivery: %w", rec.ID, err)
		}
	}
	return inserted, nil
}

// GetByID returns the stored event with id, or nil.
func (r *LocalEventRepository) GetByID(_ context.Context, id string) (*repository.NostrEventRecord, error) {
	parsed, err := nostr.IDFromHex(strings.TrimSpace(id))
	if err != nil {
		return nil, nil
	}
	for ev := range r.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{parsed}}) {
		if r.authored(ev) {
			rec := localEventRecord(ev)
			if _, found, err := r.store.Undelivered(ev); err != nil {
				return nil, err
			} else if found {
				rec.PublishState = repository.NostrPublishStateFailed
			}
			return &rec, nil
		}
	}
	return nil, nil
}

// ListByKind returns the newest stored events of kind (limit <= 0 means 50).
func (r *LocalEventRepository) ListByKind(ctx context.Context, kind int, limit int) ([]repository.NostrEventRecord, error) {
	return r.ListByKinds(ctx, []int{kind}, defaultLimit(limit, 50))
}

// ListByKinds returns the newest stored events of any of kinds (limit <= 0
// means 500).
func (r *LocalEventRepository) ListByKinds(_ context.Context, kinds []int, limit int) ([]repository.NostrEventRecord, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	return r.query(nostr.Filter{Kinds: filterKindsFromInts(kinds), Limit: defaultLimit(limit, 500)}, nil), nil
}

// FindByTag returns the newest stored events carrying the tag
// [tagName, tagValue], optionally of kinds (limit <= 0 means 50).
func (r *LocalEventRepository) FindByTag(_ context.Context, tagName, tagValue string, kinds []int, limit int) ([]repository.NostrEventRecord, error) {
	if tagName == "" || tagValue == "" {
		return nil, nil
	}
	filter := nostr.Filter{Limit: defaultLimit(limit, 50)}
	if len(kinds) > 0 {
		filter.Kinds = filterKindsFromInts(kinds)
	}
	return r.query(filter, func(ev nostr.Event) bool {
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == tagName && tag[1] == tagValue {
				return true
			}
		}
		return false
	}), nil
}

// ListByEntity is always empty: the store keeps events, not entity labels.
func (r *LocalEventRepository) ListByEntity(context.Context, string, uuid.UUID, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

// LatestCreatedAtForKinds returns the newest created_at among stored events
// of kinds.
func (r *LocalEventRepository) LatestCreatedAtForKinds(_ context.Context, kinds []int) (*time.Time, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	return r.latest(nostr.Filter{Kinds: filterKindsFromInts(kinds)}), nil
}

// LatestCreatedAtForKindsAndAuthors is LatestCreatedAtForKinds limited to
// authors.
func (r *LocalEventRepository) LatestCreatedAtForKindsAndAuthors(_ context.Context, kinds []int, authors []string) (*time.Time, error) {
	if len(kinds) == 0 || len(authors) == 0 {
		return nil, nil
	}
	converted, err := filterAuthorsFromHex(authors)
	if err != nil {
		return nil, err
	}
	return r.latest(nostr.Filter{Kinds: filterKindsFromInts(kinds), Authors: converted}), nil
}

func (r *LocalEventRepository) latest(filter nostr.Filter) *time.Time {
	filter.Limit = 1
	records := r.query(filter, nil)
	if len(records) == 0 {
		return nil
	}
	return &records[0].CreatedAt
}

// query returns the stored events matching filter (and match, applied before
// filter.Limit counts) newest first, restricted to the view's authors.
func (r *LocalEventRepository) query(filter nostr.Filter, match func(nostr.Event) bool) []repository.NostrEventRecord {
	limit := filter.Limit
	if len(r.authors) > 0 {
		filter.Authors = r.authorsWithin(filter.Authors)
		if len(filter.Authors) == 0 {
			return nil
		}
	}
	if match != nil {
		filter.Limit = 0
	}
	undelivered, err := r.store.UndeliveredEventIDs()
	if err != nil {
		// Without the markers every record would read as delivered; the
		// Projector would then dedupe against an abandoned event. Flag
		// nothing rather than fail the read, and leave the warning to the
		// readiness check that lists the markers.
		undelivered = nil
	}
	var out []repository.NostrEventRecord
	for ev := range r.store.QueryEvents(filter) {
		if match != nil && !match(ev) {
			continue
		}
		rec := localEventRecord(ev)
		if _, flagged := undelivered[ev.ID]; flagged {
			rec.PublishState = repository.NostrPublishStateFailed
		}
		out = append(out, rec)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func (r *LocalEventRepository) authored(ev nostr.Event) bool {
	if len(r.authors) == 0 {
		return true
	}
	for _, author := range r.authors {
		if author == ev.PubKey {
			return true
		}
	}
	return false
}

// authorsWithin narrows requested authors to the view's authors.
func (r *LocalEventRepository) authorsWithin(requested []nostr.PubKey) []nostr.PubKey {
	if len(requested) == 0 {
		return r.authors
	}
	var out []nostr.PubKey
	for _, pubkey := range requested {
		for _, author := range r.authors {
			if pubkey == author {
				out = append(out, pubkey)
				break
			}
		}
	}
	return out
}

func localEventRecord(ev nostr.Event) repository.NostrEventRecord {
	tags, err := json.Marshal(ev.Tags)
	if err != nil {
		tags = []byte("[]")
	}
	return repository.NostrEventRecord{
		ID:        ev.ID.Hex(),
		Kind:      int(ev.Kind),
		PubKey:    ev.PubKey.Hex(),
		Content:   ev.Content,
		Tags:      tags,
		Sig:       eventSignatureHex(&ev),
		CreatedAt: ev.CreatedAt.Time(),
	}
}

func defaultLimit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	return limit
}
