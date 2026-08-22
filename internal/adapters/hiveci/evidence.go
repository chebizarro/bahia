package hiveci

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type ReleaseObjectResolver interface {
	ResolveReleaseObject(context.Context, domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error)
}

type RepositoryReleaseEvidence struct {
	events  repository.NostrEventRepository
	hive    repository.HiveCIRepository
	workers repository.WorkerRepository
	objects ReleaseObjectResolver
	now     func() time.Time
}

func NewRepositoryReleaseEvidence(events repository.NostrEventRepository, hive repository.HiveCIRepository, workers repository.WorkerRepository, objects ReleaseObjectResolver) *RepositoryReleaseEvidence {
	return &RepositoryReleaseEvidence{
		events: events, hive: hive, workers: workers, objects: objects,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (e *RepositoryReleaseEvidence) GetWorkflowRunEvent(ctx context.Context, eventID string) (*nostr.Event, error) {
	if e == nil || e.events == nil {
		return nil, fmt.Errorf("nostr evidence repository is not configured")
	}
	return e.getSignedEvent(ctx, eventID)
}

func (e *RepositoryReleaseEvidence) getSignedEvent(ctx context.Context, eventID string) (*nostr.Event, error) {
	record, err := e.events.GetByID(ctx, eventID)
	if err != nil || record == nil {
		return nil, err
	}
	return signedEventFromRecord(record)
}

func signedEventFromRecord(record *repository.NostrEventRecord) (*nostr.Event, error) {
	if record == nil {
		return nil, nil
	}
	wire := struct {
		ID        string          `json:"id"`
		PubKey    string          `json:"pubkey"`
		CreatedAt int64           `json:"created_at"`
		Kind      int             `json:"kind"`
		Tags      json.RawMessage `json:"tags"`
		Content   string          `json:"content"`
		Sig       string          `json:"sig"`
	}{
		ID: record.ID, PubKey: record.PubKey, CreatedAt: record.CreatedAt.Unix(),
		Kind: record.Kind, Tags: record.Tags, Content: record.Content, Sig: record.Sig,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	var event nostr.Event
	if err := json.Unmarshal(encoded, &event); err != nil {
		return nil, fmt.Errorf("decode stored signed event: %w", err)
	}
	return &event, nil
}

func (e *RepositoryReleaseEvidence) ListPipelinePolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	if e == nil || e.hive == nil {
		return nil, fmt.Errorf("Hive-CI policy repository is not configured")
	}
	return e.hive.ListPolicies(ctx)
}

func (e *RepositoryReleaseEvidence) AdmitWorker(
	ctx context.Context,
	pubkey, capability, workerAdEventID string,
) (WorkerAdmissionEvidence, bool, error) {
	var evidence WorkerAdmissionEvidence
	if e == nil || e.workers == nil || e.events == nil || e.now == nil {
		return evidence, false, fmt.Errorf("worker admission evidence is not configured")
	}
	ad, err := e.getSignedEvent(ctx, workerAdEventID)
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
	worker, err := e.workers.GetByPubKey(ctx, pubkey)
	if err != nil || worker == nil {
		return evidence, false, err
	}
	if !worker.LastAdvertisementAt.Equal(ad.CreatedAt.Time()) {
		return evidence, false, fmt.Errorf("referenced worker advertisement is not the current admitted advertisement")
	}
	current := *worker
	current.Status = current.ComputeStatus(now)
	decision := service.Evaluate(service.WorkerAdmissionRequest{
		Scope: service.AdmissionScopeServiceDeploy, Worker: &current, PinnedWorker: pubkey,
	})
	evidence = WorkerAdmissionEvidence{
		WorkerIdentity: pubkey, WorkerCapability: capability, WorkerAdEventID: workerAdEventID,
		WorkerAdvertisedAt: ad.CreatedAt.Time(), DecisionCode: decision.Code,
		CapacityClass: string(decision.CapacityClass), PressureLevel: string(decision.PressureLevel),
	}
	return evidence, decision.Eligible, nil
}

func (e *RepositoryReleaseEvidence) ResolveArtifact(ctx context.Context, descriptor domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	if e == nil || e.objects == nil {
		return ResolvedReleaseArtifact{}, fmt.Errorf("release object resolver is not configured")
	}
	return e.objects.ResolveReleaseObject(ctx, descriptor)
}

var _ ReleaseEvidence = (*RepositoryReleaseEvidence)(nil)
