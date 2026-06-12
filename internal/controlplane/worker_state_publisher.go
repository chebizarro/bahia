package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// WorkerStatePublisher publishes the replaceable worker state read model used by
// control-plane clients.
type WorkerStatePublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer

	auditRepo repository.NostrEventRepository
	logger    *zap.Logger

	mu              sync.Mutex
	lastPublishedAt map[string]nostr.Timestamp
}

func NewWorkerStatePublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *WorkerStatePublisher {
	return &WorkerStatePublisher{
		publisher:       publisher,
		signer:          signer,
		logger:          zap.NewNop(),
		lastPublishedAt: make(map[string]nostr.Timestamp),
	}
}

func (p *WorkerStatePublisher) ConfigureAudit(repo repository.NostrEventRepository, logger *zap.Logger) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.auditRepo = repo
	if logger == nil {
		logger = zap.NewNop()
	}
	p.logger = logger
}

func (p *WorkerStatePublisher) Publish(ctx context.Context, worker *domain.Worker) error {
	if p == nil || p.publisher == nil {
		return fmt.Errorf("worker state publisher is not configured")
	}
	if worker == nil {
		return fmt.Errorf("worker is nil")
	}
	if worker.SchedulingState == "" {
		worker.SchedulingState = domain.WorkerSchedulingActive
	}
	content := workerStateContent(worker)
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: p.nextCreatedAt(worker.PubKey), Tags: workerStateTags(worker), Content: mustJSON(content)}
	if err := SignGoNostrEvent(ctx, p.signer, event); err != nil {
		return fmt.Errorf("sign worker state: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *event)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("publish worker state: no relay accepted the request")
	}
	p.recordAudit(ctx, event)
	return nil
}

func (p *WorkerStatePublisher) recordAudit(ctx context.Context, event *nostr.Event) {
	p.mu.Lock()
	repo := p.auditRepo
	logger := p.logger
	p.mu.Unlock()
	if repo == nil || event == nil {
		return
	}
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		logger.Warn("failed to marshal worker state event tags for audit", zap.String("event_id", event.ID.Hex()), zap.Error(err))
		return
	}
	if _, err := repo.Record(ctx, &repository.NostrEventRecord{ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(), Content: event.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(event.Sig[:]), CreatedAt: event.CreatedAt.Time(), ReceivedAt: time.Now().UTC()}); err != nil {
		logger.Warn("failed to audit worker state event", zap.String("event_id", event.ID.Hex()), zap.Error(err))
	}
}

func (p *WorkerStatePublisher) nextCreatedAt(workerPubKey string) nostr.Timestamp {
	now := nostr.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	last := p.lastPublishedAt[workerPubKey]
	if now <= last {
		now = last + 1
	}
	p.lastPublishedAt[workerPubKey] = now
	return now
}

func workerStateContent(worker *domain.Worker) map[string]any {
	return map[string]any{
		"deleted":               false,
		"pubkey":                worker.PubKey,
		"name":                  worker.Name,
		"description":           worker.Description,
		"architecture":          worker.Architecture,
		"max_concurrent_jobs":   worker.MaxConcurrentJobs,
		"current_queue_depth":   worker.CurrentQueueDepth,
		"status":                string(worker.Status),
		"scheduling_state":      string(worker.SchedulingState),
		"scheduling_note":       worker.SchedulingNote,
		"labels":                worker.Labels,
		"capabilities":          worker.Capabilities,
		"ml_capabilities":       worker.MLCapabilities,
		"runtime_target":        worker.RuntimeTarget,
		"resources":             worker.Resources,
		"accelerators":          worker.Accelerators,
		"telemetry":             worker.Telemetry,
		"pressure":              worker.Pressure,
		"last_advertisement_at": worker.LastAdvertisementAt.Format(time.RFC3339),
		"updated_at":            worker.UpdatedAt.Format(time.RFC3339),
	}
}

func workerStateTags(worker *domain.Worker) nostr.Tags {
	tags := nostr.Tags{
		{"d", "worker:state:" + worker.PubKey},
		{"domain", "worker"},
		{"schema", "bahia.state.worker.v1"},
		{"legacy_kind", fmt.Sprintf("%d", KindWorkerState)},
		{"worker", worker.PubKey},
		{"deleted", "false"},
		{"status", string(worker.Status)},
		{"scheduling_state", string(worker.SchedulingState)},
	}
	if worker.Pressure != nil {
		if worker.Pressure.CapacityClass != "" {
			tags = append(tags, nostr.Tag{"capacity_class", string(worker.Pressure.CapacityClass)})
		}
		if worker.Pressure.OverallLevel != "" {
			tags = append(tags, nostr.Tag{"pressure_state", string(worker.Pressure.OverallLevel)})
		}
		if worker.Pressure.RecommendedAction != "" {
			tags = append(tags, nostr.Tag{"recommended_action", string(worker.Pressure.RecommendedAction)})
		}
	}
	for key, value := range worker.Labels {
		if key != "" {
			tags = append(tags, nostr.Tag{"label", key, value})
		}
	}
	return tags
}
