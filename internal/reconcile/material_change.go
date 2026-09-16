package reconcile

import (
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// materialState is the canonical, semantic projection of an environment
// service state plus its latest observation. It deliberately EXCLUDES
// volatile bookkeeping that changes on every reconcile pass — LastReconciledAt,
// UpdatedAt, the rotating CurrentObservationID value, reconcile backoff/failure
// counters, and the diagnostics metadata map — so that a materially unchanged
// observation compares equal and emits no event.
//
// Everything that IS included is a real transition an operator or downstream
// projector needs to hear about exactly once.
type materialState struct {
	DriftStatus         string
	DesiredArtifactID   string
	DesiredIntentID     string
	LastSuccessfulRunID string
	DeploymentUnitID    string
	DesiredHash         string
	DesiredSpecHash     string
	HasObservationLink  bool
	Health              string
	ObservedHash        string
	ObservedDigest      string
}

// Deterministic reason codes carried on the state-changed event so downstream
// consumers can distinguish which transition fired without diffing state.
const (
	changeReasonDriftStatus         = "drift_status"
	changeReasonDesiredArtifact     = "desired_artifact"
	changeReasonDesiredIntent       = "desired_intent"
	changeReasonLastSuccessfulRun   = "last_successful_run"
	changeReasonDeploymentUnit      = "deployment_unit"
	changeReasonDesiredHash         = "desired_hash"
	changeReasonDesiredSpec         = "desired_spec"
	changeReasonObservationLink     = "observation_link"
	changeReasonHealth              = "health"
	changeReasonObservedHash        = "observed_hash"
	changeReasonObservedImageDigest = "observed_digest"
)

// materialStateOf builds the semantic projection of state + observation.
// obs may be nil (no observation yet); health/hash/digest are then empty.
func materialStateOf(state *domain.EnvironmentServiceState, obs *domain.RuntimeObservation) materialState {
	m := materialState{}
	if state == nil {
		return m
	}
	m.DriftStatus = string(state.DriftStatus)
	m.DesiredArtifactID = uuidPtrString(state.DesiredArtifactID)
	m.DesiredIntentID = uuidPtrString(state.DesiredIntentID)
	m.LastSuccessfulRunID = uuidPtrString(state.LastSuccessfulRunID)
	m.DeploymentUnitID = uuidPtrString(state.DeploymentUnitID)
	m.DesiredHash = state.DesiredHash
	if state.DesiredRuntimeState != nil {
		m.DesiredSpecHash = state.DesiredRuntimeState.ComputeDesiredHash()
	}
	// The presence of an observation link is semantic (first observation seen);
	// the specific observation ID rotating every pass is bookkeeping.
	m.HasObservationLink = state.CurrentObservationID != nil
	if obs != nil {
		m.Health = string(obs.HealthStatus)
		m.ObservedHash = observedHashOf(obs)
		m.ObservedDigest = domain.NormalizeImageDigest(obs.ObservedImageDigest)
	}
	return m
}

// observedHashOf mirrors the reconciler's own observation-hash resolution so
// the fingerprint and the drift decision never disagree about what was seen.
func observedHashOf(obs *domain.RuntimeObservation) string {
	if obs == nil {
		return ""
	}
	if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash != "" {
		return obs.NormalizedState.ObservationHash
	}
	return obs.NormalizedHash
}

// diff reports whether b differs materially from a and returns the sorted,
// deterministic list of reason codes for every component that changed.
func (a materialState) diff(b materialState) (bool, []string) {
	var reasons []string
	if a.DriftStatus != b.DriftStatus {
		reasons = append(reasons, changeReasonDriftStatus)
	}
	if a.DesiredArtifactID != b.DesiredArtifactID {
		reasons = append(reasons, changeReasonDesiredArtifact)
	}
	if a.DesiredIntentID != b.DesiredIntentID {
		reasons = append(reasons, changeReasonDesiredIntent)
	}
	if a.LastSuccessfulRunID != b.LastSuccessfulRunID {
		reasons = append(reasons, changeReasonLastSuccessfulRun)
	}
	if a.DeploymentUnitID != b.DeploymentUnitID {
		reasons = append(reasons, changeReasonDeploymentUnit)
	}
	if a.DesiredHash != b.DesiredHash {
		reasons = append(reasons, changeReasonDesiredHash)
	}
	if a.DesiredSpecHash != b.DesiredSpecHash {
		reasons = append(reasons, changeReasonDesiredSpec)
	}
	if a.HasObservationLink != b.HasObservationLink {
		reasons = append(reasons, changeReasonObservationLink)
	}
	if a.Health != b.Health {
		reasons = append(reasons, changeReasonHealth)
	}
	if a.ObservedHash != b.ObservedHash {
		reasons = append(reasons, changeReasonObservedHash)
	}
	if a.ObservedDigest != b.ObservedDigest {
		reasons = append(reasons, changeReasonObservedImageDigest)
	}
	sort.Strings(reasons)
	return len(reasons) > 0, reasons
}

// changeReasonString renders the sorted reasons as the stable comma-joined
// value carried on the event.
func changeReasonString(reasons []string) string {
	return strings.Join(reasons, ",")
}

func uuidPtrString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
