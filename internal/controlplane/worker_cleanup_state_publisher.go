package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const workerCleanupStateSchema = "bahia.state.worker-cleanup.v1"

// WorkerCleanupStatePublisher publishes cleanup execution lifecycle as canonical
// kind 30900 state so web clients can subscribe to durable cleanup status rather
// than infer status from command acknowledgments.
type WorkerCleanupStatePublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer

	auditRepo repository.NostrEventRepository
	logger    *zap.Logger

	mu              sync.Mutex
	lastPublishedAt map[string]nostr.Timestamp
}

func NewWorkerCleanupStatePublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *WorkerCleanupStatePublisher {
	return &WorkerCleanupStatePublisher{
		publisher:       publisher,
		signer:          signer,
		logger:          zap.NewNop(),
		lastPublishedAt: make(map[string]nostr.Timestamp),
	}
}

func (p *WorkerCleanupStatePublisher) ConfigureAudit(repo repository.NostrEventRepository, logger *zap.Logger) {
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

func (p *WorkerCleanupStatePublisher) Publish(ctx context.Context, cleanup events.WorkerCleanupEvent) error {
	if p == nil || p.publisher == nil {
		return fmt.Errorf("worker cleanup state publisher is not configured")
	}
	if strings.TrimSpace(cleanup.WorkerPubKey) == "" {
		return fmt.Errorf("worker cleanup state requires worker pubkey")
	}
	if strings.TrimSpace(cleanup.Status) == "" {
		return fmt.Errorf("worker cleanup state requires status")
	}

	id := workerCleanupStateID(cleanup)
	event := &nostr.Event{
		Kind:      KindCASControlState,
		CreatedAt: p.nextCreatedAt(id),
		Tags:      workerCleanupStateTags(id, cleanup),
		Content:   mustJSON(workerCleanupStateContent(id, cleanup)),
	}
	if err := SignGoNostrEvent(ctx, p.signer, event); err != nil {
		return fmt.Errorf("sign worker cleanup state: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *event)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("publish worker cleanup state: no relay accepted the request")
	}
	p.recordAudit(ctx, event)
	return nil
}

func workerCleanupStateID(cleanup events.WorkerCleanupEvent) string {
	workerPubKey := strings.TrimSpace(cleanup.WorkerPubKey)
	if cleanup.LoomJobID != "" {
		return "worker:cleanup:" + workerPubKey + ":" + cleanup.LoomJobID
	}
	startedAt := cleanup.StartedAt.UTC().Format(time.RFC3339Nano)
	if startedAt == "0001-01-01T00:00:00Z" {
		startedAt = cleanup.Status
	}
	return "worker:cleanup:" + workerPubKey + ":" + startedAt
}

func workerCleanupStateContent(id string, cleanup events.WorkerCleanupEvent) map[string]any {
	updatedAt := cleanup.StartedAt
	if cleanup.CompletedAt != nil {
		updatedAt = *cleanup.CompletedAt
	}
	content := map[string]any{
		"deleted":           false,
		"cleanup_id":        id,
		"worker_pubkey":     cleanup.WorkerPubKey,
		"cleanup_mode":      cleanup.CleanupMode,
		"reason":            cleanup.Reason,
		"loom_job_id":       cleanup.LoomJobID,
		"protected_refs":    cleanup.ProtectedRefs,
		"target_free_gb":    cleanup.TargetFreeGB,
		"status":            cleanup.Status,
		"capacity_rejected": cleanup.CapacityRejected,
		"error":             cleanup.Error,
		"started_at":        cleanup.StartedAt.UTC().Format(time.RFC3339),
		"updated_at":        updatedAt.UTC().Format(time.RFC3339),
	}
	if cleanup.CompletedAt != nil {
		content["completed_at"] = cleanup.CompletedAt.UTC().Format(time.RFC3339)
	}
	return content
}

func workerCleanupStateTags(id string, cleanup events.WorkerCleanupEvent) nostr.Tags {
	tags := nostr.Tags{
		{"d", id},
		{"domain", "worker"},
		{"schema", workerCleanupStateSchema},
		{"worker", cleanup.WorkerPubKey},
		{"status", cleanup.Status},
		{"cleanup_mode", cleanup.CleanupMode},
		{"deleted", "false"},
	}
	if cleanup.LoomJobID != "" {
		tags = append(tags, nostr.Tag{"loom_job", cleanup.LoomJobID})
	}
	if cleanup.CapacityRejected {
		tags = append(tags, nostr.Tag{"capacity_rejected", "true"})
	}
	return tags
}

func (p *WorkerCleanupStatePublisher) recordAudit(ctx context.Context, event *nostr.Event) {
	p.mu.Lock()
	repo := p.auditRepo
	logger := p.logger
	p.mu.Unlock()
	if repo == nil || event == nil {
		return
	}
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		logger.Warn("failed to marshal worker cleanup state event tags for audit", zap.String("event_id", event.ID), zap.Error(err))
		return
	}
	if _, err := repo.Record(ctx, &repository.NostrEventRecord{ID: event.ID, Kind: event.Kind, PubKey: event.PubKey, Content: event.Content, Tags: tagsJSON, Sig: event.Sig, CreatedAt: event.CreatedAt.Time(), ReceivedAt: time.Now().UTC()}); err != nil {
		logger.Warn("failed to audit worker cleanup state event", zap.String("event_id", event.ID), zap.Error(err))
	}
}

func (p *WorkerCleanupStatePublisher) nextCreatedAt(id string) nostr.Timestamp {
	now := nostr.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	last := p.lastPublishedAt[id]
	if now <= last {
		now = last + 1
	}
	p.lastPublishedAt[id] = now
	return now
}
