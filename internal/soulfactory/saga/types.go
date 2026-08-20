package saga

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Stage is authoritative durable provisioning state.
type Stage string

const (
	StageRequested         Stage = "requested"
	StageIdentityReserved  Stage = "identity_reserved"
	StageRuntimeAllocated  Stage = "runtime_allocated"
	StageSignerEnrolled    Stage = "signer_enrolled"
	StageNostrConfigured   Stage = "nostr_configured"
	StageLLMVerified       Stage = "llm_verified"
	StageDMVerified        Stage = "dm_verified"
	StageRunning           Stage = "running"
	StageRollbackPending   Stage = "rollback_pending"
	StageRolledBack        Stage = "rolled_back"
	StageFailedRecoverable Stage = "failed_recoverable"
	StageFailedTerminal    Stage = "failed_terminal"
)

const (
	SystemBahiaProjection      = "bahia_projection"
	ResourceProvisioningResult = "provisioning_result_7950"
	ResourceAgentSoul          = "agent_soul_31951"
)

var forwardStages = []Stage{
	StageIdentityReserved, StageRuntimeAllocated, StageSignerEnrolled,
	StageNostrConfigured, StageLLMVerified, StageDMVerified, StageRunning,
}

func ForwardStages() []Stage { return append([]Stage(nil), forwardStages...) }

func validStage(s Stage) bool {
	for _, candidate := range append([]Stage{StageRequested, StageRollbackPending, StageRolledBack, StageFailedRecoverable, StageFailedTerminal}, forwardStages...) {
		if s == candidate {
			return true
		}
	}
	return false
}

// Ownership determines whether compensation is allowed. Only saga-created resources may be removed.
type Ownership string

const (
	OwnershipCreated     Ownership = "created"
	OwnershipAdopted     Ownership = "adopted"
	OwnershipPreExisting Ownership = "pre_existing"
)

// CompensationRank fixes reverse dependency order independent of stage implementation.
type CompensationRank int

const (
	CompensateSignetPolicy CompensationRank = 100
	CompensateCredentials  CompensationRank = 200
	CompensateRuntime      CompensationRank = 300
	CompensateContainer    CompensationRank = 400
	CompensateProjection   CompensationRank = 500
)

// Resource records immutable, secret-free ownership lineage for one side effect.
type Resource struct {
	Stage              Stage            `json:"stage"`
	System             string           `json:"system"`
	Kind               string           `json:"kind"`
	ExternalID         string           `json:"external_id"`
	SpecHash           string           `json:"spec_hash"`
	Ownership          Ownership        `json:"ownership"`
	OwnerRunID         string           `json:"owner_run_id,omitempty"`
	IdempotencyKey     string           `json:"idempotency_key"`
	CorrelationID      string           `json:"correlation_id"`
	AuthoritativeStage Stage            `json:"authoritative_stage,omitempty"`
	CompensationOrder  CompensationRank `json:"compensation_order"`
	RecordedAt         time.Time        `json:"recorded_at"`
}

func (r Resource) key() string { return strings.Join([]string{r.System, r.Kind, r.ExternalID}, "\x00") }

func (r Resource) validate(runID string) error {
	if !validStage(r.Stage) || strings.TrimSpace(r.System) == "" || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.ExternalID) == "" || strings.TrimSpace(r.SpecHash) == "" || strings.TrimSpace(r.IdempotencyKey) == "" || strings.TrimSpace(r.CorrelationID) == "" {
		return errors.New("resource lineage is incomplete")
	}
	if containsSecretMarker(r.System) || containsSecretMarker(r.Kind) || containsSecretMarker(r.ExternalID) || containsSecretMarker(r.SpecHash) || containsSecretMarker(r.IdempotencyKey) || containsSecretMarker(r.CorrelationID) {
		return errors.New("resource lineage contains secret-shaped data")
	}
	if !strings.HasPrefix(r.ExternalID, "ref:sha256:") || len(r.ExternalID) != len("ref:sha256:")+64 {
		return errors.New("resource external id must be a one-way public reference")
	}
	if r.System == SystemBahiaProjection && (r.Kind == ResourceProvisioningResult || r.Kind == ResourceAgentSoul) && !r.AuthoritativeStage.terminalProjection() {
		return errors.New("terminal projection lineage has invalid authoritative stage")
	}
	switch r.Ownership {
	case OwnershipCreated:
		if r.OwnerRunID != runID {
			return errors.New("created resource owner does not match saga run")
		}
	case OwnershipAdopted, OwnershipPreExisting:
		if r.OwnerRunID != "" {
			return errors.New("non-created resource cannot be owned by saga run")
		}
	default:
		return errors.New("resource ownership is invalid")
	}
	return nil
}

// PublicResourceRef irreversibly converts an external identifier to secret-free lineage.
func PublicResourceRef(system, kind, externalID string) string {
	if strings.HasPrefix(externalID, "ref:sha256:") && len(externalID) == len("ref:sha256:")+64 {
		if _, err := hex.DecodeString(strings.TrimPrefix(externalID, "ref:sha256:")); err == nil {
			return externalID
		}
	}
	sum := sha256.Sum256([]byte("bahia-openclaw-resource/v1\x00" + system + "\x00" + kind + "\x00" + externalID))
	return "ref:sha256:" + hex.EncodeToString(sum[:])
}

func (s Stage) terminalProjection() bool {
	return s == StageRunning || s == StageRolledBack || s == StageFailedTerminal
}

func containsSecretMarker(value string) bool {
	v := strings.ToLower(value)
	for _, marker := range []string{"nsec1", "secret=", "token=", "api_key=", "private_key=", "bunker://"} {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return false
}

// Failure is a deliberately public, secret-free terminal/retry record.
type Failure struct {
	Stage     Stage     `json:"stage"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	At        time.Time `json:"at"`
}

// Transition is an append-only audit checkpoint.
type Transition struct {
	From Stage     `json:"from"`
	To   Stage     `json:"to"`
	At   time.Time `json:"at"`
}

// Compensation is an append-only record of inspected rollback reality.
type Compensation struct {
	ResourceKey    string    `json:"resource_key"`
	IdempotencyKey string    `json:"idempotency_key"`
	Outcome        string    `json:"outcome"`
	At             time.Time `json:"at"`
}

// Run is the complete durable saga checkpoint. RequestID and RunID form the immutable root identity.
type Run struct {
	RequestID     string         `json:"request_id"`
	RunID         string         `json:"run_id"`
	RootKey       string         `json:"root_key"`
	AgentID       string         `json:"agent_id"`
	SpecHash      string         `json:"spec_hash"`
	Stage         Stage          `json:"stage"`
	ResumeStage   Stage          `json:"resume_stage,omitempty"`
	Version       uint64         `json:"version"`
	Resources     []Resource     `json:"resources,omitempty"`
	Compensations []Compensation `json:"compensations,omitempty"`
	Transitions   []Transition   `json:"transitions,omitempty"`
	Failure       *Failure       `json:"failure,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	RetainUntil   *time.Time     `json:"retain_until,omitempty"`
}

func NewRun(requestID, runID, agentID, specHash string, now time.Time) (*Run, error) {
	requestID, runID, agentID, specHash = strings.TrimSpace(requestID), strings.TrimSpace(runID), strings.TrimSpace(agentID), strings.TrimSpace(specHash)
	if requestID == "" || runID == "" || agentID == "" || specHash == "" {
		return nil, errors.New("request id, run id, agent id, and spec hash are required")
	}
	if containsSecretMarker(requestID) || containsSecretMarker(runID) || containsSecretMarker(agentID) || containsSecretMarker(specHash) {
		return nil, errors.New("saga identity contains secret-shaped data")
	}
	return &Run{RequestID: requestID, RunID: runID, RootKey: DeriveKey(requestID+"/"+runID, "root"), AgentID: agentID, SpecHash: specHash, Stage: StageRequested, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

func DeriveKey(root, purpose string) string {
	sum := sha256.Sum256([]byte("bahia-openclaw-saga/v1\x00" + root + "\x00" + purpose))
	return "ocw-saga:" + hex.EncodeToString(sum[:])
}

func (r *Run) StageKey(stage Stage) string { return DeriveKey(r.RootKey, "stage/"+string(stage)) }
func (r *Run) CompensationKey(resource Resource) string {
	return DeriveKey(r.RootKey, "compensate/"+resource.key())
}

func (r *Run) clone() *Run {
	if r == nil {
		return nil
	}
	out := *r
	out.Resources = append([]Resource(nil), r.Resources...)
	out.Compensations = append([]Compensation(nil), r.Compensations...)
	out.Transitions = append([]Transition(nil), r.Transitions...)
	if r.Failure != nil {
		f := *r.Failure
		out.Failure = &f
	}
	if r.RetainUntil != nil {
		t := *r.RetainUntil
		out.RetainUntil = &t
	}
	return &out
}

func (r *Run) validate() error {
	if r == nil || r.RequestID == "" || r.RunID == "" || r.RootKey == "" || r.AgentID == "" || r.SpecHash == "" || !validStage(r.Stage) || r.Version == 0 {
		return errors.New("saga checkpoint is invalid")
	}
	if r.RootKey != DeriveKey(r.RequestID+"/"+r.RunID, "root") {
		return errors.New("saga root key does not match immutable request/run identity")
	}
	seen := map[string]Resource{}
	for _, resource := range r.Resources {
		if err := resource.validate(r.RunID); err != nil {
			return err
		}
		if resource.SpecHash != r.SpecHash {
			return fmt.Errorf("resource spec does not match saga request for %s/%s", resource.System, resource.Kind)
		}
		if resource.CorrelationID != r.RequestID {
			return fmt.Errorf("resource correlation does not match saga request for %s/%s", resource.System, resource.Kind)
		}
		if previous, ok := seen[resource.key()]; ok && (previous.SpecHash != resource.SpecHash || previous.Ownership != resource.Ownership || previous.OwnerRunID != resource.OwnerRunID) {
			return fmt.Errorf("immutable lineage conflict for %s/%s", resource.System, resource.Kind)
		}
		seen[resource.key()] = resource
	}
	return nil
}

func sortForCompensation(resources []Resource) {
	sort.SliceStable(resources, func(i, j int) bool { return resources[i].CompensationOrder < resources[j].CompensationOrder })
}

func hasTerminalProjection(resources []Resource, run *Run, stage Stage) bool {
	result, soul := false, false
	for _, resource := range resources {
		if resource.System != SystemBahiaProjection || resource.SpecHash != run.SpecHash || resource.CorrelationID != run.RequestID || resource.AuthoritativeStage != stage {
			continue
		}
		result = result || resource.Kind == ResourceProvisioningResult
		soul = soul || resource.Kind == ResourceAgentSoul
	}
	return result && soul
}
