package hiveci

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
)

type ReleaseObjectResolver interface {
	ResolveReleaseObject(context.Context, domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error)
}

// EvidenceStore is the local event store: the verified relay copy of the
// signed events release admission is decided on. *localstore.Store satisfies
// it.
type EvidenceStore interface {
	SaveEvent(nostr.Event) (bool, error)
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

// PipelinePolicySource lists the canonical pipeline policies.
type PipelinePolicySource interface {
	ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
}

// WorkerSchedulingSource reports the operator scheduling state of a worker.
type WorkerSchedulingSource interface {
	WorkerSchedulingState(ctx context.Context, pubkey string) (domain.WorkerSchedulingState, error)
}

// LocalReleaseEvidence resolves everything release admission checks from the
// local event store: the signed 5401 lineage, the worker's
// signed advertisement, and the canonical pipeline policies. No SQL mirror is
// read, so a missing or stale row can neither reject evidence the relays hold
// nor admit evidence they do not.
type LocalReleaseEvidence struct {
	events     EvidenceStore
	policies   PipelinePolicySource
	scheduling WorkerSchedulingSource
	objects    ReleaseObjectResolver
	thresholds service.WorkerPressureThresholds
	now        func() time.Time
}

// NewLocalReleaseEvidence returns release evidence over the local event store.
// scheduling may be nil, in which case no operator override applies.
func NewLocalReleaseEvidence(events EvidenceStore, policies PipelinePolicySource, scheduling WorkerSchedulingSource, objects ReleaseObjectResolver, thresholds service.WorkerPressureThresholds) *LocalReleaseEvidence {
	return &LocalReleaseEvidence{
		events: events, policies: policies, scheduling: scheduling, objects: objects,
		thresholds: thresholds, now: func() time.Time { return time.Now().UTC() },
	}
}

func (e *LocalReleaseEvidence) GetWorkflowRunEvent(_ context.Context, eventID string) (*nostr.Event, error) {
	if e == nil || e.events == nil {
		return nil, fmt.Errorf("local evidence store is not configured")
	}
	return storedEvent(e.events, eventID, kinds.HiveCIWorkflowRun)
}

// storedEvent returns the stored event with id and kind, or nil. The caller
// validates its signature: the store is a cache, not a trust boundary.
func storedEvent(store EvidenceStore, eventID string, kind int) (*nostr.Event, error) {
	id, err := nostr.IDFromHex(strings.TrimSpace(eventID))
	if err != nil {
		return nil, nil
	}
	for ev := range store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}, Kinds: []nostr.Kind{nostr.Kind(kind)}}) {
		found := ev
		return &found, nil
	}
	return nil, nil
}

func (e *LocalReleaseEvidence) ListPipelinePolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	if e == nil || e.policies == nil {
		return nil, fmt.Errorf("Hive-CI pipeline policy view is not configured")
	}
	return e.policies.ListPolicies(ctx)
}

// AdmitWorker admits the release worker on its own signed advertisement. The
// advertisement is replaceable, so the store holds only the worker's current
// one: a release that names an older advertisement finds nothing and is not
// admitted. Capacity and pressure come from what the worker signed; the only
// other input is the operator's scheduling state, read from the daemon's
// retained worker-state record.
func (e *LocalReleaseEvidence) AdmitWorker(
	ctx context.Context,
	pubkey, capability, workerAdEventID string,
) (WorkerAdmissionEvidence, bool, error) {
	var evidence WorkerAdmissionEvidence
	if e == nil || e.events == nil || e.now == nil {
		return evidence, false, fmt.Errorf("worker admission evidence is not configured")
	}
	ad, err := storedEvent(e.events, workerAdEventID, kinds.LoomWorkerAdvertisement)
	if err != nil {
		return evidence, false, err
	}
	if ad == nil {
		return evidence, false, fmt.Errorf("referenced signed worker advertisement is missing")
	}
	now := e.now().UTC()
	if err := nostradapter.ValidateInboundEvent(ad, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return evidence, false, fmt.Errorf("worker advertisement signature boundary: %w", err)
	}
	if int(ad.Kind) != kinds.LoomWorkerAdvertisement || ad.ID.Hex() != workerAdEventID ||
		ad.PubKey.Hex() != pubkey {
		return evidence, false, fmt.Errorf("worker advertisement identity does not match signed 5401")
	}
	signedCapability := make(nostr.Tags, 0)
	for _, tag := range ad.Tags {
		if len(tag) >= 2 && (tag[0] == "S" || tag[0] == "A") {
			signedCapability = append(signedCapability, append(nostr.Tag(nil), tag...))
		}
	}
	encodedCapability, err := json.Marshal(signedCapability)
	if err != nil {
		return evidence, false, fmt.Errorf("encode signed worker capability: %w", err)
	}
	if len(signedCapability) == 0 || strings.TrimSpace(capability) != string(encodedCapability) {
		return evidence, false, fmt.Errorf("worker capability does not match referenced signed advertisement")
	}
	// The store collapses replaceable events, but it is asked rather than
	// assumed: a newer advertisement by the same worker supersedes this one.
	for current := range e.events.QueryEvents(nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(kinds.LoomWorkerAdvertisement)}, Authors: []nostr.PubKey{ad.PubKey}, Limit: 1,
	}) {
		if current.ID != ad.ID {
			return evidence, false, fmt.Errorf("referenced worker advertisement is not the current admitted advertisement")
		}
	}
	worker := nostradapter.WorkerFromAdvertisement(ad)
	if e.scheduling != nil {
		state, err := e.scheduling.WorkerSchedulingState(ctx, pubkey)
		if err != nil {
			return evidence, false, fmt.Errorf("worker scheduling state: %w", err)
		}
		worker.SchedulingState = state
	}
	worker.Pressure = service.AssessWithThresholds(*worker, now, e.thresholds)
	worker.Status = worker.ComputeStatus(now)
	decision := service.Evaluate(service.WorkerAdmissionRequest{
		Scope: service.AdmissionScopeServiceDeploy, Worker: worker, PinnedWorker: pubkey,
	})
	evidence = WorkerAdmissionEvidence{
		WorkerIdentity: pubkey, WorkerCapability: capability, WorkerAdEventID: workerAdEventID,
		WorkerAdvertisedAt: ad.CreatedAt.Time(), DecisionCode: decision.Code,
		CapacityClass: string(decision.CapacityClass), PressureLevel: string(decision.PressureLevel),
	}
	return evidence, decision.Eligible, nil
}

func (e *LocalReleaseEvidence) ResolveArtifact(ctx context.Context, descriptor domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	if e == nil || e.objects == nil {
		return ResolvedReleaseArtifact{}, fmt.Errorf("release object resolver is not configured")
	}
	return e.objects.ResolveReleaseObject(ctx, descriptor)
}

var _ ReleaseEvidence = (*LocalReleaseEvidence)(nil)
