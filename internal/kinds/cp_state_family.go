package kinds

import "strconv"

// CPStateFamily is the family discriminator a canonical 30900 cp-state record
// carries in its legacy_kind tag (CASControlStateTagLegacyKind). Its values are
// the retired per-family catalog kinds; they identify a record family inside
// the 30900 envelope and are never a wire kind to publish or subscribe on.
//
// This file is the single sanctioned reference to those numbers for the
// discriminator contract: producers and consumers name a CPStateFamily
// instead of the legacy kind constants, and the architecture ratchet
// (internal/archtest, legacy-kind gate) exempts values of this type. Phase 1
// (bahia-irsry.9) replaces the numeric discriminator with family ids.
type CPStateFamily int

// DNS cp-state families (legacy_kind 31975-31978).
const (
	CPStateFamilyDNSZone     CPStateFamily = DNSZoneState
	CPStateFamilyDNSEndpoint CPStateFamily = DNSEndpointState
	CPStateFamilyDNSPolicy   CPStateFamily = DNSPolicyState
	CPStateFamilyDNSBackend  CPStateFamily = DNSBackendState
)

// Worker cp-state families (bahia-irsry.9.2). 32000-32003 are the retired
// WorkerState* catalog kinds; 32004 names worker cleanup execution, which never
// had a wire kind. Every worker record is published on 30900 only; the retired
// wire kinds are decoded solely by internal/nostrmigration.
const (
	CPStateFamilyWorkerState       CPStateFamily = 32000
	CPStateFamilyWorkerAssignment  CPStateFamily = 32001
	CPStateFamilyWorkerDrain       CPStateFamily = 32002
	CPStateFamilyWorkerEligibility CPStateFamily = 32003
	CPStateFamilyWorkerCleanup     CPStateFamily = 32004
)

// Worker cp-state coordinates (bahia-irsry.36). Every worker family addresses
// its records under its own "worker:<entity>:" prefix. Relays keep one event
// per (kind, pubkey, d), and assignment and drain were both keyed by the bare
// worker pubkey, so each family replaced the other on the relay. Live records
// and tombstones share the family coordinate.
const (
	WorkerStateDPrefix       = "worker:state:"
	WorkerAssignmentDPrefix  = "worker:assignment:"
	WorkerDrainDPrefix       = "worker:drain:"
	WorkerEligibilityDPrefix = "worker:eligibility:"
	WorkerCleanupDPrefix     = "worker:cleanup:"
)

var workerDPrefixes = map[CPStateFamily]string{
	CPStateFamilyWorkerState:       WorkerStateDPrefix,
	CPStateFamilyWorkerAssignment:  WorkerAssignmentDPrefix,
	CPStateFamilyWorkerDrain:       WorkerDrainDPrefix,
	CPStateFamilyWorkerEligibility: WorkerEligibilityDPrefix,
	CPStateFamilyWorkerCleanup:     WorkerCleanupDPrefix,
}

// WorkerDPrefix returns the d prefix of a worker family's coordinates, and
// false for a family that is not a worker family.
func (f CPStateFamily) WorkerDPrefix() (string, bool) {
	prefix, ok := workerDPrefixes[f]
	return prefix, ok
}

// WorkerDTag is the canonical d builder for worker cp-state records: the
// family's prefix followed by the record id (the worker pubkey for state,
// assignment and drain; the preview id for eligibility; "<pubkey>:<run>" for
// cleanup). It returns false for a family that is not a worker family.
func (f CPStateFamily) WorkerDTag(id string) (string, bool) {
	prefix, ok := f.WorkerDPrefix()
	if !ok {
		return "", false
	}
	return prefix + id, true
}

// LegacyKind returns the numeric discriminator for producer APIs that still
// key records by the catalog kind (the projector's control-state envelope).
func (f CPStateFamily) LegacyKind() int { return int(f) }

// TagValue returns the legacy_kind tag value consumers match on.
func (f CPStateFamily) TagValue() string { return strconv.Itoa(int(f)) }
