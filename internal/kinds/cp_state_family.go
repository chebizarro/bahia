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

// Payment and security cp-state families (bahia-irsry.60). These records are
// confidential (OCK-encrypted) and published through the shared cp-state
// envelope with the controlStateEnvelope/publishControlState pipeline.
const (
	CPStateFamilyPaymentRecord         CPStateFamily = PaymentRecord
	CPStateFamilySecurityFinding       CPStateFamily = SecurityFindingRecord
	CPStateFamilySecuritySchedule      CPStateFamily = SecurityScheduleRecord
	CPStateFamilySecurityFindingDetail CPStateFamily = SecurityFindingDetailRecord
	CPStateFamilyManagedInstanceHealth CPStateFamily = ManagedInstanceHealthRecord
	CPStateFamilyRouteCanary           CPStateFamily = RouteCanaryRecord
	CPStateFamilySoulRuntimePolicy     CPStateFamily = SoulRuntimePolicyRecord
	CPStateFamilyBlossomAdmin          CPStateFamily = BlossomAdminRecord
	CPStateFamilyBlossomBlob           CPStateFamily = BlossomBlobRecord
)

// Security scan execution families (audit B-32). A target record carries the
// scan input and a run record is the signed claim and durable progress of one
// scan, so the daemon schedules and resumes scans from its local event store
// instead of SQL leases. Like worker cleanup, these families never had a wire
// kind: they exist only as 30900 discriminators, so they are declared here and
// not in the kind catalog.
const (
	CPStateFamilySecurityTarget CPStateFamily = 32020
	CPStateFamilySecurityRun    CPStateFamily = 32021
)

// Adoption binding family (audit B-35). One record per adopted workload binds
// its runtime fingerprints to the service, environment, deployment unit, build
// and artifact the adoption published for it, and carries the adoption's
// durable progress. The daemon resolves an adopted workload's identity and
// resumes an interrupted adoption from these records in its local event store
// instead of the SQL adopted_runtime_identity table. Like the security scan
// families, it never had a wire kind and exists only as a 30900 discriminator.
// 32022-32025 and 32027 are taken by the HiveCI and SoulFactory saga families.
const CPStateFamilyAdoptionBinding CPStateFamily = 32026

// AdoptionBindingDTag is the d of the adoption binding of a service in an
// environment: "adoption:binding:<service-id>:<environment-id>".
func AdoptionBindingDTag(serviceID, environmentID string) string {
	return "adoption:binding:" + serviceID + ":" + environmentID
}

// ServiceStateDTag is the d of a service's state in an environment:
// "service:<service-id>:environment:<environment-id>". The publishers of the
// family and the readers that look a state up in the local event store both
// build the coordinate here.
func ServiceStateDTag(serviceID, environmentID string) string {
	return "service:" + serviceID + ":environment:" + environmentID
}

// RuntimeObservationDTag is the d of the runtime observation of a service in
// an environment: "runtime:observation:<service-id>:<environment-id>".
func RuntimeObservationDTag(serviceID, environmentID string) string {
	return "runtime:observation:" + serviceID + ":" + environmentID
}

// Hive-CI execution families (audit C-48, C-49, bahia-xjdo9). A policy
// record is the pipeline policy release admission is checked against, a
// result record is the daemon's processing state of one signed kind-5402
// result, an initiation record is the journal of one build initiation (its
// prepared signed events, with the per-run publisher key in the service-only
// layer), and a release record is the accepted-release ledger: the daemon's
// admission of one signed kind-4903 release attestation under a release
// identity (or the quarantine of an attestation that conflicts with it), so
// admission, retry, initiation and release replay detection resume from the
// local event store with no SQL row. Like the security execution families
// these never had a wire kind: they exist only as 30900 discriminators and
// are not in the kind catalog. 32026 is the adoption binding family.
const (
	CPStateFamilyHiveCIPolicy     CPStateFamily = 32022
	CPStateFamilyHiveCIResult     CPStateFamily = 32023
	CPStateFamilyHiveCIInitiation CPStateFamily = 32024
	CPStateFamilyHiveCIRelease    CPStateFamily = 32027
)

// CPStateFamilySoulFactorySagaRun is the canonical progress record of one
// governed Soul Factory provisioning saga run (audit C-45): its stage,
// ownership lineage, compensations and current failure, replaced per run so
// a daemon moved to a fresh host resumes from its local event store instead
// of a local checkpoint file. Like the security families above it has no
// wire kind of its own: it is a 30900 discriminator only.
const CPStateFamilySoulFactorySagaRun CPStateFamily = 32025

// CPStateFamilySoulFactoryAdapterLedger is the canonical adapter ledger of
// governed Soul Factory provisioning (bahia-nfc95): one replaceable record
// per provisioning request (the resolved request, the Soul projection, the
// registry identifiers and the per-step resource references the production
// adapters resume from) and one per reserved agent identity. The records
// are fleet-OCK encrypted; the retained signed success result is in the
// service-only layer. 32026 is the adoption binding and 32027 the HiveCI
// release ledger. Like the saga-run family it is a 30900 discriminator only.
const CPStateFamilySoulFactoryAdapterLedger CPStateFamily = 32028

// CPStateFamilyOperatorAllowlist is the daemon's published copy of its
// operator allowlists (bahia-fbyo5): one replaceable record per scope,
// `operators:continuity` from nostr.authorized_pubkeys and
// `operators:soul-factory` from soul_factory.authorized_pubkeys. The content
// is fleet-OCK encrypted and the record carries no pubkey in any tag, so a
// browser holding the fleet OCK can trust the other authorized operators'
// documents without the relay learning who the operators are. Like the
// families above it has no wire kind of its own: it is a 30900 discriminator
// only. 32028 is the SoulFactory adapter ledger.
const CPStateFamilyOperatorAllowlist CPStateFamily = 32029

// Operator allowlist scopes. Each names the daemon config list the record
// mirrors and the operator-authored kinds it authorizes.
const (
	// OperatorAllowlistScopeContinuity mirrors nostr.authorized_pubkeys: the
	// signers of continuity definitions (31400-31404), failover/recovery
	// commands (38430/38431) and continuity heartbeats (30315).
	OperatorAllowlistScopeContinuity = "continuity"
	// OperatorAllowlistScopeSoulFactory mirrors soul_factory.authorized_pubkeys:
	// the signers of SoulFactory drafts (31952), actions (1950) and the fleet
	// configuration (31953).
	OperatorAllowlistScopeSoulFactory = "soul-factory"
)

// OperatorAllowlistDPrefix prefixes every operator allowlist coordinate.
const OperatorAllowlistDPrefix = "operators:"

// OperatorAllowlistDTag is the d of one scope's allowlist record:
// "operators:<scope>". The scope is public; the pubkeys are only in the
// encrypted content.
func OperatorAllowlistDTag(scope string) string {
	return OperatorAllowlistDPrefix + scope
}

// FleetOCKScope is the well-known orgID value used for fleet-wide
// confidential cp-state (payments, security findings/schedules). The OCK
// for this scope is wrapped to all fleet operators (config authorized_pubkeys
// plus bootstrap_owners) so they can decrypt in the web dashboard.
const FleetOCKScope = "fleet"
