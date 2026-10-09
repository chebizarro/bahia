package domain

import "github.com/google/uuid"

// RuntimeObservationMateriallyChanged compares the durable runtime transition,
// excluding generated identity, sampling time, and diagnostic metadata.
func RuntimeObservationMateriallyChanged(previous, current *RuntimeObservation) bool {
	if previous == nil || current == nil {
		return previous != current
	}
	return observationUUID(previous.DeploymentUnitID) != observationUUID(current.DeploymentUnitID) ||
		previous.ObservedImageRepo != current.ObservedImageRepo ||
		NormalizeImageDigest(previous.ObservedImageDigest) != NormalizeImageDigest(current.ObservedImageDigest) ||
		previous.ObservedContainerID != current.ObservedContainerID ||
		previous.ObservedHost != current.ObservedHost ||
		previous.ObservedVersion != current.ObservedVersion ||
		previous.HealthStatus != current.HealthStatus ||
		previous.Source != current.Source ||
		RuntimeObservationHash(previous) != RuntimeObservationHash(current) ||
		observationContentHash(previous) != observationContentHash(current)
}

func observationUUID(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// RuntimeObservationHash resolves the same effective hash used by drift decisions.
func RuntimeObservationHash(obs *RuntimeObservation) string {
	if obs == nil {
		return ""
	}
	if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash != "" {
		return obs.NormalizedState.ObservationHash
	}
	return obs.NormalizedHash
}

func observationContentHash(obs *RuntimeObservation) string {
	if obs.NormalizedState == nil {
		return ""
	}
	state := *obs.NormalizedState
	return state.ComputeObservationHash()
}
