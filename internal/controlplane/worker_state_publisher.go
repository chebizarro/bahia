package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// WorkerStatePublisher publishes the worker-state read model used by
// control-plane clients as a canonical 30900 cp-state record
// (legacy_kind kinds.CPStateFamilyWorkerState, t kinds.WorkerStateTopic).
type WorkerStatePublisher struct {
	publisher NostrEventPublisher
	signer    nostr.Signer

	auditRepo repository.NostrEventRepository
	logger    *zap.Logger

	mu              sync.Mutex
	lastPublishedAt map[string]nostr.Timestamp
}

func NewWorkerStatePublisher(publisher NostrEventPublisher, signer nostr.Signer) *WorkerStatePublisher {
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
	content, err := workerStateContent(worker)
	if err != nil {
		return err
	}
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: p.nextCreatedAt(worker.PubKey), Tags: workerStateTags(worker, false), Content: mustJSON(content)}
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

// workerStateUnpublishedFields are domain.Worker fields kept off the relay:
// verified execution planes are live reconciliation evidence (plane, session
// and probe ids) owned by the daemon, not worker state for clients.
var workerStateUnpublishedFields = []string{"verified_execution_planes"}

// workerStateContent is the domain.Worker JSON (minus
// workerStateUnpublishedFields) plus deleted=false. It is lossless on purpose:
// the relay projection cache replays this record into the worker repository
// (catalog decodeWorkerProjection -> workerApplier), whose upsert overwrites
// every advertised column, so a partial record would erase software, pricing
// or FIPS mesh fields on replay.
func workerStateContent(worker *domain.Worker) (map[string]any, error) {
	encoded, err := json.Marshal(worker)
	if err != nil {
		return nil, fmt.Errorf("encode worker state: %w", err)
	}
	content := map[string]any{}
	if err := json.Unmarshal(encoded, &content); err != nil {
		return nil, fmt.Errorf("encode worker state: %w", err)
	}
	for _, field := range workerStateUnpublishedFields {
		delete(content, field)
	}
	content["deleted"] = false
	return content, nil
}

// workerStateDTag is the worker-state record's coordinate, built by the
// canonical worker d builder ("worker:state:<pubkey>", unchanged from the
// per-family schema, so envelope records replace earlier ones in place).
func workerStateDTag(workerPubKey string) string {
	d, _ := kinds.CPStateFamilyWorkerState.WorkerDTag(workerPubKey)
	return d
}

func workerStateTags(worker *domain.Worker, deleted bool) nostr.Tags {
	tags := workerCPStateEnvelope(kinds.CPStateFamilyWorkerState, kinds.WorkerStateTopic, workerStateDTag(worker.PubKey), deleted)
	tags = append(tags,
		nostr.Tag{"worker", worker.PubKey},
		nostr.Tag{"status", string(worker.Status)},
		nostr.Tag{"scheduling_state", string(worker.SchedulingState)},
	)
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
