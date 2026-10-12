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
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
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

// InitiationRecord is the journal of one build initiation: the prepared
// signed events, pinned job arguments and the per-run publisher key survive
// process loss. Its canonical home is the daemon's confidential initiation
// record in the local event store (CanonicalInitiationStore). Upstream and
// mirror-read passwords are never stored here.
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

func newInitiationRecord(req controlplane.HiveCIBuildStartRequest) (*InitiationRecord, error) {
	req.SourceEventID = strings.TrimSpace(req.SourceEventID)
	if req.SourceEventID == "" {
		return nil, fmt.Errorf("build initiation requires a source event ID")
	}
	// The build identity is derived from the signed source event, so two
	// initiators or a restart name the same build without a claim.
	if req.BuildID == uuid.Nil {
		req.BuildID = controlplane.BuildIDForSourceEvent(req.SourceEventID)
	}
	identity := req
	identity.BuildID = uuid.Nil // The identity is the request, not the id.
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(encoded)
	return &InitiationRecord{SourceEventID: req.SourceEventID, Fingerprint: hex.EncodeToString(fingerprint[:]),
		Stage: StageClaimed, Request: req, Result: controlplane.HiveCIBuildStartResult{BuildID: req.BuildID}}, nil
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
