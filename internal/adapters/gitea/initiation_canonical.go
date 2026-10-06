package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

// InitiationJournal is the daemon's confidential initiation record family in
// the local event store. *nostrAdapter.HiveCICanonicalPublisher satisfies it.
type InitiationJournal interface {
	PublishInitiation(ctx context.Context, sourceEventID, stage, buildID string, document, serviceOnly []byte) error
	ReadInitiation(ctx context.Context, sourceEventID string) (*nostrAdapter.HiveCIInitiationEntry, error)
	ListInitiations(ctx context.Context) ([]nostrAdapter.HiveCIInitiationEntry, error)
}

// InitiationIndex is the optional PostgreSQL copy of the journal.
// *PgInitiationStore satisfies it.
type InitiationIndex interface {
	Upsert(context.Context, *InitiationRecord) error
	ListInFlight(context.Context) ([]*InitiationRecord, error)
}

// initiationSecrets is the service-only layer of a journal record: material
// the daemon alone may read back. It is NIP-44 encrypted to the service
// pubkey inside the fleet-OCK envelope and never appears in the document.
type initiationSecrets struct {
	PublisherNsec string `json:"publisher_nsec,omitempty"`
}

// CanonicalInitiationStore keeps build initiations as confidential records
// in the local event store (audit C-49). The build identity is derived from
// the signed source event, so there is nothing to claim: Claim publishes the
// record of a request the store has not seen and adopts the retained record
// otherwise, and Advance is a stage compare-and-publish against the retained
// record. A restart, or a daemon with no database, resumes from the journal
// the outbox made durable. The optional SQL index is written after the
// record is published and its failures are logged.
type CanonicalInitiationStore struct {
	journal InitiationJournal
	index   InitiationIndex
	// mu serializes read-check-publish sequences on this process, so two
	// callers cannot both decide a stage is theirs to advance.
	mu     sync.Mutex
	logger *zap.Logger
}

var _ InitiationStore = (*CanonicalInitiationStore)(nil)

// NewCanonicalInitiationStore returns the store over journal. index may be
// nil.
func NewCanonicalInitiationStore(journal InitiationJournal, index InitiationIndex, logger *zap.Logger) *CanonicalInitiationStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CanonicalInitiationStore{journal: journal, index: index, logger: logger.Named("hiveci-initiation-journal")}
}

func (s *CanonicalInitiationStore) available() error {
	if s == nil || s.journal == nil {
		return errors.New("build initiation journal is not configured")
	}
	return nil
}

func encodeInitiation(rec *InitiationRecord) (document, serviceOnly []byte, err error) {
	public := *rec
	public.PublisherNsec = ""
	document, err = json.Marshal(public)
	if err != nil {
		return nil, nil, err
	}
	if nsec := strings.TrimSpace(rec.PublisherNsec); nsec != "" {
		serviceOnly, err = json.Marshal(initiationSecrets{PublisherNsec: nsec})
		if err != nil {
			return nil, nil, err
		}
	}
	return document, serviceOnly, nil
}

func decodeInitiation(entry *nostrAdapter.HiveCIInitiationEntry) (*InitiationRecord, error) {
	var rec InitiationRecord
	if err := json.Unmarshal(entry.Document, &rec); err != nil {
		return nil, fmt.Errorf("decode build initiation: %w", err)
	}
	if len(entry.ServiceOnly) > 0 {
		var secrets initiationSecrets
		if err := json.Unmarshal(entry.ServiceOnly, &secrets); err != nil {
			return nil, fmt.Errorf("decode build initiation service layer: %w", err)
		}
		rec.PublisherNsec = secrets.PublisherNsec
	}
	if rec.SourceEventID != entry.SourceEventID || string(rec.Stage) != entry.Stage || rec.Request.BuildID.String() != entry.BuildID || rec.Result.BuildID != rec.Request.BuildID {
		return nil, fmt.Errorf("build initiation identity or stage mismatch")
	}
	return &rec, nil
}

func (s *CanonicalInitiationStore) publish(ctx context.Context, rec *InitiationRecord) error {
	document, serviceOnly, err := encodeInitiation(rec)
	if err != nil {
		return err
	}
	if err := s.journal.PublishInitiation(ctx, rec.SourceEventID, string(rec.Stage), rec.Request.BuildID.String(), document, serviceOnly); err != nil {
		return fmt.Errorf("journal build initiation: %w", err)
	}
	if s.index != nil {
		if err := s.index.Upsert(ctx, rec); err != nil {
			s.logger.Warn("build initiation SQL index write failed; canonical journal retained", zap.String("source_event_id", rec.SourceEventID), zap.Error(err))
		}
	}
	return nil
}

func (s *CanonicalInitiationStore) read(ctx context.Context, sourceEventID string) (*InitiationRecord, error) {
	entry, err := s.journal.ReadInitiation(ctx, sourceEventID)
	if err != nil || entry == nil {
		return nil, err
	}
	return decodeInitiation(entry)
}

// Claim journals the initiation of a request the store has not seen and
// returns it as claimed; for a request it has, it returns the retained record
// (whatever its stage) after checking the request is the same one.
func (s *CanonicalInitiationStore) Claim(ctx context.Context, req controlplane.HiveCIBuildStartRequest) (*InitiationRecord, bool, error) {
	if err := s.available(); err != nil {
		return nil, false, err
	}
	req.SourceEventID = strings.TrimSpace(req.SourceEventID)
	if req.SourceEventID == "" {
		return nil, false, fmt.Errorf("build initiation requires a source event ID")
	}
	derived := controlplane.BuildIDForSourceEvent(req.SourceEventID)
	if req.BuildID != uuid.Nil && req.BuildID != derived {
		return nil, false, fmt.Errorf("build id %s does not belong to source event %s", req.BuildID, req.SourceEventID)
	}
	req.BuildID = derived
	rec, err := newInitiationRecord(req)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.read(ctx, rec.SourceEventID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.Fingerprint != rec.Fingerprint {
			return nil, false, fmt.Errorf("source event conflicts with its canonical build initiation")
		}
		return existing, false, nil
	}
	if err := s.publish(ctx, rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

func (s *CanonicalInitiationStore) Get(ctx context.Context, sourceEventID string) (*InitiationRecord, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	return s.read(ctx, strings.TrimSpace(sourceEventID))
}

// Advance publishes rec if the retained record is still at from for the same
// build; otherwise the stage was taken by another initiator and the caller
// must re-read.
func (s *CanonicalInitiationStore) Advance(ctx context.Context, from InitiationStage, rec *InitiationRecord) error {
	if err := s.available(); err != nil {
		return err
	}
	if !validInitiationTransition(from, rec.Stage) {
		return fmt.Errorf("invalid build initiation transition %s -> %s", from, rec.Stage)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.read(ctx, rec.SourceEventID)
	if err != nil {
		return err
	}
	if current == nil || current.Stage != from || current.Request.BuildID != rec.Request.BuildID {
		return ErrInitiationConflict
	}
	return s.publish(ctx, rec)
}

// BackfillFromIndex journals, once, the in-flight initiations that exist only
// in the SQL index because they were claimed before the journal was
// canonical. The marker is written only after everything was admitted.
func (s *CanonicalInitiationStore) BackfillFromIndex(ctx context.Context, marker BackfillMarker) error {
	if s == nil || s.index == nil || marker == nil {
		return nil
	}
	if err := s.available(); err != nil {
		return err
	}
	if done, err := marker.GetControlRecord("bootstrap", initiationBackfillMarker); err != nil || string(done) == "1" {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inFlight, err := s.index.ListInFlight(ctx)
	if err != nil {
		return err
	}
	for _, rec := range inFlight {
		existing, err := s.read(ctx, rec.SourceEventID)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		document, serviceOnly, err := encodeInitiation(rec)
		if err != nil {
			return err
		}
		if err := s.journal.PublishInitiation(ctx, rec.SourceEventID, string(rec.Stage), rec.Request.BuildID.String(), document, serviceOnly); err != nil {
			return fmt.Errorf("backfill build initiation %s: %w", rec.SourceEventID, err)
		}
	}
	return marker.PutControlRecord("bootstrap", initiationBackfillMarker, []byte("1"))
}

// RebuildIndex writes every retained journal record into the SQL index.
func (s *CanonicalInitiationStore) RebuildIndex(ctx context.Context) error {
	if s == nil || s.index == nil {
		return nil
	}
	if err := s.available(); err != nil {
		return err
	}
	entries, err := s.journal.ListInitiations(ctx)
	if err != nil {
		return err
	}
	var failed []error
	for i := range entries {
		rec, err := decodeInitiation(&entries[i])
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if err := s.index.Upsert(ctx, rec); err != nil {
			failed = append(failed, fmt.Errorf("initiation %s: %w", rec.SourceEventID, err))
		}
	}
	return errors.Join(failed...)
}

// BackfillMarker records that the one-time SQL-era backfill ran.
// *localstore.Outbox satisfies it.
type BackfillMarker interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}

const initiationBackfillMarker = "hiveci-initiation-canonical-v1"
