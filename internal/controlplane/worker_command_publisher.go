package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
)

const (
	WorkerCommandCordon           = "worker.cordon.request"
	WorkerCommandUncordon         = "worker.uncordon.request"
	WorkerCommandDrain            = "worker.drain.request"
	WorkerCommandUndrain          = "worker.undrain.request"
	WorkerCommandMaintenanceEnter = "worker.maintenance.enter.request"
	WorkerCommandMaintenanceExit  = "worker.maintenance.exit.request"
	WorkerCommandLabelsUpdate     = "worker.labels.update.request"
)

// WorkerCommandPublisher emits canonical worker-management command events.
type WorkerCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewWorkerCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *WorkerCommandPublisher {
	return &WorkerCommandPublisher{publisher: publisher, signer: signer}
}

type WorkerLifecycleCommand struct {
	WorkerPubKey     string         `json:"worker_pubkey"`
	Reason           string         `json:"reason,omitempty"`
	OperatorMetadata map[string]any `json:"operator_metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
}

type WorkerLabelsUpdateCommand struct {
	WorkerPubKey     string            `json:"worker_pubkey"`
	Labels           map[string]string `json:"labels"`
	Reason           string            `json:"reason,omitempty"`
	OperatorMetadata map[string]any    `json:"operator_metadata,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	AgentID          string            `json:"agent_id,omitempty"`
}

type WorkerCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	StateKind       int    `json:"state_kind"`
	DTag            string `json:"d_tag"`
	PublishedRelays int    `json:"published_relays"`
	WorkerPubKey    string `json:"worker_pubkey"`
	Command         string `json:"command"`
}

func (p *WorkerCommandPublisher) PublishWorkerCordonRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerCordonRequest, WorkerCommandCordon, "worker-cordon", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerUncordonRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerUncordonRequest, WorkerCommandUncordon, "worker-uncordon", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerDrainRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerDrainRequest, WorkerCommandDrain, "worker-drain", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerUndrainRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerUndrainRequest, WorkerCommandUndrain, "worker-undrain", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerMaintenanceEnterRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerMaintenanceEnter, WorkerCommandMaintenanceEnter, "worker-maintenance-enter", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerMaintenanceExitRequest(ctx context.Context, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	return p.publishLifecycle(ctx, KindWorkerMaintenanceExit, WorkerCommandMaintenanceExit, "worker-maintenance-exit", cmd)
}

func (p *WorkerCommandPublisher) PublishWorkerLabelsUpdateRequest(ctx context.Context, cmd WorkerLabelsUpdateCommand) (*WorkerCommandReceipt, error) {
	content := map[string]any{
		"worker_pubkey":     strings.TrimSpace(cmd.WorkerPubKey),
		"reason":            cmd.Reason,
		"operator_metadata": cmd.OperatorMetadata,
		"idempotency_key":   strings.TrimSpace(cmd.IdempotencyKey),
		"labels":            cmd.Labels,
	}
	return p.publish(ctx, KindWorkerLabelsUpdate, WorkerCommandLabelsUpdate, "worker-labels-update", cmd.WorkerPubKey, cmd.IdempotencyKey, cmd.AgentID, content)
}

func (p *WorkerCommandPublisher) publishLifecycle(ctx context.Context, kind int, command, defaultPrefix string, cmd WorkerLifecycleCommand) (*WorkerCommandReceipt, error) {
	content := map[string]any{
		"worker_pubkey":     strings.TrimSpace(cmd.WorkerPubKey),
		"reason":            cmd.Reason,
		"operator_metadata": cmd.OperatorMetadata,
		"idempotency_key":   strings.TrimSpace(cmd.IdempotencyKey),
	}
	return p.publish(ctx, kind, command, defaultPrefix, cmd.WorkerPubKey, cmd.IdempotencyKey, cmd.AgentID, content)
}

func (p *WorkerCommandPublisher) publish(ctx context.Context, kind int, command, defaultPrefix, workerPubKey, dTag, agentID string, content map[string]any) (*WorkerCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("worker command publisher is not configured")
	}
	workerPubKey = strings.TrimSpace(workerPubKey)
	if workerPubKey == "" {
		return nil, fmt.Errorf("worker_pubkey is required")
	}
	dTag = strings.TrimSpace(dTag)
	if dTag == "" {
		dTag = defaultPrefix + ":" + uuid.NewString()
		content["idempotency_key"] = dTag
	}
	tags := nostr.Tags{{"d", dTag}, {"worker", workerPubKey}, {"command", command}}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		tags = append(tags, nostr.Tag{"agent", agentID})
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal worker command content: %w", err)
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign worker command event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		return nil, fmt.Errorf("publish worker command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish worker command event: no relay accepted the request")
	}
	return &WorkerCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: kind, StatusKind: KindWorkerStatus, ResultKind: KindWorkerResult, StateKind: KindWorkerState, DTag: dTag, PublishedRelays: published, WorkerPubKey: workerPubKey, Command: command}, nil
}
