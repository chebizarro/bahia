package hiveci

import (
	"context"
	"encoding/json"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type ReleaseObjectResolver interface {
	ResolveReleaseObject(context.Context, domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error)
}

type RepositoryReleaseEvidence struct {
	events  repository.NostrEventRepository
	hive    repository.HiveCIRepository
	workers repository.WorkerRepository
	objects ReleaseObjectResolver
}

func NewRepositoryReleaseEvidence(events repository.NostrEventRepository, hive repository.HiveCIRepository, workers repository.WorkerRepository, objects ReleaseObjectResolver) *RepositoryReleaseEvidence {
	return &RepositoryReleaseEvidence{events: events, hive: hive, workers: workers, objects: objects}
}

func (e *RepositoryReleaseEvidence) GetWorkflowRunEvent(ctx context.Context, eventID string) (*nostr.Event, error) {
	if e == nil || e.events == nil {
		return nil, fmt.Errorf("nostr evidence repository is not configured")
	}
	record, err := e.events.GetByID(ctx, eventID)
	if err != nil || record == nil {
		return nil, err
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
		return nil, fmt.Errorf("decode stored signed workflow event: %w", err)
	}
	return &event, nil
}

func (e *RepositoryReleaseEvidence) ListPipelinePolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	if e == nil || e.hive == nil {
		return nil, fmt.Errorf("Hive-CI policy repository is not configured")
	}
	return e.hive.ListPolicies(ctx)
}

func (e *RepositoryReleaseEvidence) IsWorkerAdmitted(ctx context.Context, pubkey, _ string) (bool, error) {
	if e == nil || e.workers == nil {
		return false, fmt.Errorf("worker admission repository is not configured")
	}
	worker, err := e.workers.GetByPubKey(ctx, pubkey)
	if err != nil || worker == nil {
		return false, err
	}
	return worker.Status == domain.WorkerStatusOnline &&
		(worker.SchedulingState == "" || worker.SchedulingState == domain.WorkerSchedulingActive), nil
}

func (e *RepositoryReleaseEvidence) ResolveArtifact(ctx context.Context, descriptor domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	if e == nil || e.objects == nil {
		return ResolvedReleaseArtifact{}, fmt.Errorf("release object resolver is not configured")
	}
	return e.objects.ResolveReleaseObject(ctx, descriptor)
}

var _ ReleaseEvidence = (*RepositoryReleaseEvidence)(nil)
