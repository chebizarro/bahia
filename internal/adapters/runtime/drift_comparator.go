// Package runtime provides runtime observation adapters for querying actual deployment state.
package runtime

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Drift comparator — compares desired hash against normalized observed hash
// to determine drift state for desired-state-managed Compose/Docker workloads.
// ---------------------------------------------------------------------------

// DriftState represents the result of comparing desired state against
// observed runtime state for a single service.
type DriftState struct {
	// Status is the drift determination: in_sync, drifted, or unknown.
	Status domain.DriftStatus `json:"status"`

	// DesiredHash is the deterministic hash from the DesiredServiceSpec.
	DesiredHash string `json:"desired_hash"`

	// ObservedHash is the normalized observation hash from the runtime.
	// Empty when observation is unavailable.
	ObservedHash string `json:"observed_hash,omitempty"`

	// HealthStatus is the observed health of the running service.
	// Empty/unknown when observation is unavailable.
	HealthStatus domain.HealthStatus `json:"health_status,omitempty"`

	// Reason provides a human-readable explanation of the drift determination.
	Reason string `json:"reason"`
}

// ---------------------------------------------------------------------------
// DriftObserver abstracts observation so the comparator can be tested without
// real Docker/Compose runtimes.
// ---------------------------------------------------------------------------

// DriftObserver provides the observation needed for drift comparison.
// It returns a RuntimeObservation with optional NormalizedState, or an error
// if the observation cannot be performed.
type DriftObserver interface {
	// ObserveForDrift returns the current runtime observation for a service.
	// Implementations should attempt best-effort observation even on partial
	// failures. Returning (nil, nil) means the service was not found.
	ObserveForDrift(ctx context.Context, spec *domain.DesiredServiceSpec) (*domain.RuntimeObservation, error)
}

// ---------------------------------------------------------------------------
// CompareDrift — the core drift comparison function
// ---------------------------------------------------------------------------

// CompareDrift compares the desired hash from a DesiredServiceSpec against
// the normalized observed hash from a runtime observer. It determines whether
// the service is in_sync, drifted, or unknown.
//
// Drift rules:
//   - unknown: observation unavailable (observer error, nil observation, or
//     missing normalized state/hash)
//   - drifted: desired hash differs from observed hash
//   - in_sync: hashes match AND health is acceptable (healthy or starting)
//   - drifted: hashes match but health is unacceptable (unhealthy, stopped, unknown)
//
// Observation failures are not returned as errors — they produce an "unknown"
// drift state. Only programming errors (nil spec) return errors.
func CompareDrift(ctx context.Context, spec *domain.DesiredServiceSpec, observer DriftObserver) (*DriftState, error) {
	if spec == nil {
		return nil, fmt.Errorf("drift comparator: desired service spec is nil")
	}

	desiredHash := spec.DesiredHash
	if desiredHash == "" {
		// Compute if not already set.
		desiredHash = spec.ComputeDesiredHash()
	}

	// Attempt observation — failures produce unknown, not error.
	obs, err := observer.ObserveForDrift(ctx, spec)
	if err != nil {
		return &DriftState{
			Status:      domain.DriftStatusUnknown,
			DesiredHash: desiredHash,
			Reason:      fmt.Sprintf("observation failed: %v", err),
		}, nil
	}

	if obs == nil {
		return &DriftState{
			Status:      domain.DriftStatusUnknown,
			DesiredHash: desiredHash,
			Reason:      "service not found in runtime",
		}, nil
	}

	// Extract normalized hash — prefer NormalizedState.ObservationHash,
	// fall back to NormalizedHash on the observation itself.
	observedHash := ""
	if obs.NormalizedState != nil {
		observedHash = obs.NormalizedState.ObservationHash
	}
	if observedHash == "" {
		observedHash = obs.NormalizedHash
	}

	if observedHash == "" {
		return &DriftState{
			Status:       domain.DriftStatusUnknown,
			DesiredHash:  desiredHash,
			HealthStatus: obs.HealthStatus,
			Reason:       "normalized observation hash unavailable",
		}, nil
	}

	// Compare hashes.
	if desiredHash != observedHash {
		return &DriftState{
			Status:       domain.DriftStatusDrifted,
			DesiredHash:  desiredHash,
			ObservedHash: observedHash,
			HealthStatus: obs.HealthStatus,
			Reason:       "desired hash differs from observed hash",
		}, nil
	}

	// Hashes match — check health for full in_sync determination.
	if isAcceptableHealth(obs.HealthStatus) {
		return &DriftState{
			Status:       domain.DriftStatusInSync,
			DesiredHash:  desiredHash,
			ObservedHash: observedHash,
			HealthStatus: obs.HealthStatus,
			Reason:       "desired hash matches observed hash with acceptable health",
		}, nil
	}

	// Hashes match but health is not acceptable — report as drifted.
	return &DriftState{
		Status:       domain.DriftStatusDrifted,
		DesiredHash:  desiredHash,
		ObservedHash: observedHash,
		HealthStatus: obs.HealthStatus,
		Reason:       fmt.Sprintf("hashes match but health is %s", obs.HealthStatus),
	}, nil
}

// isAcceptableHealth returns true for health states that are compatible with
// an in_sync drift status. Healthy and starting are acceptable because a
// service that just started may not have completed its healthcheck yet.
func isAcceptableHealth(health domain.HealthStatus) bool {
	switch health {
	case domain.HealthStatusHealthy, domain.HealthStatusStarting:
		return true
	default:
		return false
	}
}
