package gitea

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

type InitiationStage string

const (
	StageClaimed             InitiationStage = "claimed"
	StageRequestReady        InitiationStage = "request_ready"
	StageRequestUnconfirmed  InitiationStage = "request_unconfirmed"
	StageRequestPublished    InitiationStage = "request_published"
	StageJobUnconfirmed      InitiationStage = "job_unconfirmed"
	StageJobPublished        InitiationStage = "job_published"
	StageEvidenceReady       InitiationStage = "evidence_ready"
	StageEvidenceUnconfirmed InitiationStage = "evidence_unconfirmed"
	StageEvidencePublished   InitiationStage = "evidence_published"
)

var (
	ErrInitiationConflict = errors.New("build initiation changed concurrently; retry the same source event")
	ErrPublishUnconfirmed = errors.New("build initiation publish is unconfirmed; inspect relay evidence before proceeding")
)

// InitiationRecord is local authority, not a rebuildable relay projection.
// Prepared signed events and the per-run publisher key survive process loss.
// The PostgreSQL store encrypts the entire document; upstream and mirror-read
// passwords are never stored here.
type InitiationRecord struct {
	SourceEventID           string
	Fingerprint             string
	Stage                   InitiationStage
	Request                 controlplane.HiveCIBuildStartRequest
	Result                  controlplane.HiveCIBuildStartResult
	RunRequestID            string
	RunEvent                *nostr.Event
	EvidenceEvent           *nostr.Event
	LoomJob                 *loomAdapter.JobRequest
	LoomJobID               string
	PublisherNsec           string
	MirrorRepository        string
	MirrorReadCredentialRef string
	MirrorReadUsername      string
}

type InitiationStore interface {
	// Claim atomically inserts the initial record. A losing insert returns the
	// existing canonical record, never replaces its build ID or request.
	Claim(context.Context, controlplane.HiveCIBuildStartRequest) (*InitiationRecord, bool, error)
	Get(context.Context, string) (*InitiationRecord, error)
	// Advance is a single-statement stage CAS. Zero updated rows is a conflict.
	Advance(context.Context, InitiationStage, *InitiationRecord) error
}

type initiationCipher interface {
	Encrypt(string, domain.EncryptionMethod) ([]byte, error)
	Decrypt([]byte, domain.EncryptionMethod) (string, error)
}

type PgInitiationStore struct {
	pool   *pgxpool.Pool
	cipher initiationCipher
}

func NewPgInitiationStore(pool *pgxpool.Pool, cipher initiationCipher) *PgInitiationStore {
	return &PgInitiationStore{pool: pool, cipher: cipher}
}

func newInitiationRecord(req controlplane.HiveCIBuildStartRequest) (*InitiationRecord, error) {
	req.SourceEventID = strings.TrimSpace(req.SourceEventID)
	if req.SourceEventID == "" || req.BuildID == uuid.Nil {
		return nil, fmt.Errorf("build initiation requires a source event ID and build ID")
	}
	identity := req
	identity.BuildID = uuid.Nil // The first claim owns the canonical build ID.
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(encoded)
	return &InitiationRecord{SourceEventID: req.SourceEventID, Fingerprint: hex.EncodeToString(fingerprint[:]),
		Stage: StageClaimed, Request: req, Result: controlplane.HiveCIBuildStartResult{BuildID: req.BuildID}}, nil
}

func (s *PgInitiationStore) Claim(ctx context.Context, req controlplane.HiveCIBuildStartRequest) (*InitiationRecord, bool, error) {
	rec, err := newInitiationRecord(req)
	if err != nil {
		return nil, false, err
	}
	document, err := s.encode(rec)
	if err != nil {
		return nil, false, err
	}
	var id string
	err = s.pool.QueryRow(ctx, `INSERT INTO hiveci_initiations (source_event_id, build_id, stage, document)
		VALUES ($1, $2, $3, $4) ON CONFLICT (source_event_id) DO NOTHING RETURNING source_event_id`,
		rec.SourceEventID, rec.Request.BuildID, rec.Stage, document).Scan(&id)
	if err == nil {
		return rec, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("claim build initiation: %w", err)
	}
	existing, err := s.Get(ctx, rec.SourceEventID)
	if err != nil {
		return nil, false, err
	}
	if existing == nil || existing.Fingerprint != rec.Fingerprint {
		return nil, false, fmt.Errorf("source event conflicts with its canonical build initiation")
	}
	return existing, false, nil
}

func (s *PgInitiationStore) Get(ctx context.Context, sourceEventID string) (*InitiationRecord, error) {
	var document []byte
	var stage InitiationStage
	var buildID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT build_id, stage, document FROM hiveci_initiations WHERE source_event_id=$1`, sourceEventID).Scan(&buildID, &stage, &document)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load build initiation: %w", err)
	}
	plaintext, err := s.cipher.Decrypt(document, domain.EncryptionAES256)
	if err != nil {
		return nil, fmt.Errorf("decrypt build initiation: %w", err)
	}
	var rec InitiationRecord
	if err := json.Unmarshal([]byte(plaintext), &rec); err != nil {
		return nil, fmt.Errorf("decode build initiation: %w", err)
	}
	if rec.SourceEventID != sourceEventID || rec.Request.BuildID != buildID || rec.Result.BuildID != buildID || rec.Stage != stage {
		return nil, fmt.Errorf("build initiation identity or stage mismatch")
	}
	return &rec, nil
}

func (s *PgInitiationStore) Advance(ctx context.Context, from InitiationStage, rec *InitiationRecord) error {
	if !validInitiationTransition(from, rec.Stage) {
		return fmt.Errorf("invalid build initiation transition %s -> %s", from, rec.Stage)
	}
	document, err := s.encode(rec)
	if err != nil {
		return err
	}
	var id string
	err = s.pool.QueryRow(ctx, `UPDATE hiveci_initiations SET stage=$3, document=$4, updated_at=now()
		WHERE source_event_id=$1 AND stage=$2 AND build_id=$5 RETURNING source_event_id`,
		rec.SourceEventID, from, rec.Stage, document, rec.Request.BuildID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInitiationConflict
	}
	if err != nil {
		return fmt.Errorf("advance build initiation: %w", err)
	}
	return nil
}

func (s *PgInitiationStore) encode(rec *InitiationRecord) ([]byte, error) {
	if s.pool == nil || s.cipher == nil {
		return nil, fmt.Errorf("durable build initiation store requires database and encryption")
	}
	document, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return s.cipher.Encrypt(string(document), domain.EncryptionAES256)
}

func validInitiationTransition(from, to InitiationStage) bool {
	switch from {
	case StageClaimed:
		return to == StageRequestReady || to == StageRequestPublished
	case StageRequestReady:
		return to == StageRequestUnconfirmed
	case StageRequestUnconfirmed:
		return to == StageRequestPublished
	case StageRequestPublished:
		return to == StageJobUnconfirmed || to == StageJobPublished
	case StageJobUnconfirmed:
		return to == StageJobPublished
	case StageJobPublished:
		return to == StageEvidenceReady
	case StageEvidenceReady:
		return to == StageEvidenceUnconfirmed
	case StageEvidenceUnconfirmed:
		return to == StageEvidencePublished
	default:
		return false
	}
}
