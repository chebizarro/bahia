package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// WorkerReadModelPublisher publishes worker assignment state and drain status
// read models as canonical 30900 cp-state records directly at the mutation
// site, ensuring each material change publishes exactly once.
type WorkerReadModelPublisher struct {
	publisher NostrEventPublisher
	signer    nostr.Signer
	source    WorkerReadModelSource
	logger    *zap.Logger

	mu              sync.Mutex
	lastPublishedAt map[string]nostr.Timestamp
}

// WorkerReadModelSource provides the read models to publish.
type WorkerReadModelSource interface {
	GetAssignmentState(ctx context.Context, workerPubKey string) (*domain.WorkerAssignmentState, error)
	GetDrainStatus(ctx context.Context, workerPubKey string) (*domain.WorkerDrainStatus, error)
}

func NewWorkerReadModelPublisher(publisher NostrEventPublisher, signer nostr.Signer, source WorkerReadModelSource, logger *zap.Logger) *WorkerReadModelPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &WorkerReadModelPublisher{
		publisher:       publisher,
		signer:          signer,
		source:          source,
		logger:          logger,
		lastPublishedAt: make(map[string]nostr.Timestamp),
	}
}

// PublishForWorker publishes the assignment state and drain status read models
// for the given worker. It is called directly from the code that changes
// assignment or drain state, ensuring exactly-once publication per material
// change. If the worker has no assignment or drain data, the method is a no-op.
func (p *WorkerReadModelPublisher) PublishForWorker(ctx context.Context, workerPubKey string) {
	if p == nil || p.source == nil || strings.TrimSpace(workerPubKey) == "" {
		return
	}
	assignment, err := p.source.GetAssignmentState(ctx, workerPubKey)
	if err != nil {
		p.logger.Warn("read worker assignment state for direct publish failed",
			zap.String("worker", workerPubKey), zap.Error(err))
	} else if assignment != nil {
		if err := p.publishAssignmentState(ctx, assignment); err != nil {
			p.logger.Warn("publish worker assignment state failed",
				zap.String("worker", workerPubKey), zap.Error(err))
		}
	}
	drain, err := p.source.GetDrainStatus(ctx, workerPubKey)
	if err != nil {
		p.logger.Warn("read worker drain status for direct publish failed",
			zap.String("worker", workerPubKey), zap.Error(err))
	} else if drain != nil {
		if err := p.publishDrainStatus(ctx, drain); err != nil {
			p.logger.Warn("publish worker drain status failed",
				zap.String("worker", workerPubKey), zap.Error(err))
		}
	}
}

func (p *WorkerReadModelPublisher) publishAssignmentState(ctx context.Context, state *domain.WorkerAssignmentState) error {
	if state == nil || state.WorkerPubKey == "" {
		return nil
	}
	d, _ := kinds.CPStateFamilyWorkerAssignment.WorkerDTag(state.WorkerPubKey)
	tags := workerCPStateEnvelope(kinds.CPStateFamilyWorkerAssignment, kinds.WorkerAssignmentTopic, d, false)
	tags = append(tags,
		nostr.Tag{"worker", state.WorkerPubKey},
		nostr.Tag{"assignment_count", fmt.Sprintf("%d", len(state.ActiveAssignments))},
	)
	for _, assignment := range state.ActiveAssignments {
		if assignment.Type != "" {
			tags = append(tags, nostr.Tag{"assignment_type", string(assignment.Type)})
		}
		if assignment.WorkloadID != "" {
			tags = append(tags, nostr.Tag{"workload", assignment.WorkloadID})
		}
		if assignment.Status != "" {
			tags = append(tags, nostr.Tag{"status", assignment.Status})
		}
		if assignment.Pinned {
			tags = append(tags, nostr.Tag{"pinned", "true"})
		}
	}
	return p.publishSigned(ctx, state, tags, "worker_assignment_state.direct_publish")
}

func (p *WorkerReadModelPublisher) publishDrainStatus(ctx context.Context, status *domain.WorkerDrainStatus) error {
	if status == nil || status.WorkerPubKey == "" {
		return nil
	}
	d, _ := kinds.CPStateFamilyWorkerDrain.WorkerDTag(status.WorkerPubKey)
	tags := workerCPStateEnvelope(kinds.CPStateFamilyWorkerDrain, kinds.WorkerDrainTopic, d, false)
	tags = append(tags,
		nostr.Tag{"worker", status.WorkerPubKey},
		nostr.Tag{"scheduling_state", string(status.SchedulingState)},
		nostr.Tag{"safe_to_enter_maintenance", fmt.Sprintf("%t", status.SafeToEnterMaintenance)},
		nostr.Tag{"safe_to_disable", fmt.Sprintf("%t", status.SafeToDisable)},
		nostr.Tag{"remaining", fmt.Sprintf("%d", len(status.RemainingAssignments))},
		nostr.Tag{"pinned_blockers", fmt.Sprintf("%d", len(status.PinnedBlockers))},
	)
	return p.publishSigned(ctx, status, tags, "worker_drain_status.direct_publish")
}

// PublishEligibilityPreview publishes a worker eligibility preview record.
func (p *WorkerReadModelPublisher) publishEligibilityPreview(ctx context.Context, preview *domain.WorkerEligibilityPreview) error {
	if p == nil || preview == nil || preview.PreviewID == "" {
		return nil
	}
	d, _ := kinds.CPStateFamilyWorkerEligibility.WorkerDTag(preview.PreviewID)
	tags := workerCPStateEnvelope(kinds.CPStateFamilyWorkerEligibility, kinds.WorkerEligibilityTopic, d, false)
	tags = append(tags, nostr.Tag{"preview_id", preview.PreviewID})
	if preview.WorkloadType != "" {
		tags = append(tags, nostr.Tag{"workload_type", preview.WorkloadType})
	}
	if preview.SelectedWinner != nil && preview.SelectedWinner.WorkerPubKey != "" {
		tags = append(tags, nostr.Tag{"worker", preview.SelectedWinner.WorkerPubKey})
	}
	return p.publishSigned(ctx, preview, tags, "worker_eligibility_preview.direct_publish")
}

func (p *WorkerReadModelPublisher) publishSigned(ctx context.Context, value any, tags nostr.Tags, entityType string) error {
	content, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", entityType, err)
	}
	event := &nostr.Event{
		Kind:      KindCASControlState,
		CreatedAt: p.nextCreatedAt(entityType),
		Tags:      tags,
		Content:   string(content),
	}
	if err := SignGoNostrEvent(ctx, p.signer, event); err != nil {
		return fmt.Errorf("sign %s: %w", entityType, err)
	}
	published, err := p.publisher.Publish(ctx, *event)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("publish %s: no relay accepted the event", entityType)
	}
	return nil
}

func (p *WorkerReadModelPublisher) nextCreatedAt(key string) nostr.Timestamp {
	now := nostr.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	last := p.lastPublishedAt[key]
	if now <= last {
		now = last + 1
	}
	p.lastPublishedAt[key] = now
	return now
}
