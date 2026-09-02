package controlplane

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
)

// Canonical ContextVM methods served by the per-host maintenance driver
// (cascadia-nips schema cascadia.maintenance.v1, epic fp-jan).
const (
	ContextVMMethodMaintenanceScan       = "maintenance/scan"
	ContextVMMethodMaintenanceReport     = "maintenance/report"
	ContextVMMethodMaintenanceQuarantine = "maintenance/quarantine"
	ContextVMMethodMaintenanceRestore    = "maintenance/restore"
	ContextVMMethodMaintenanceRelocate   = "maintenance/relocate"
	ContextVMMethodMaintenancePurge      = "maintenance/purge"
	ContextVMMethodMaintenanceGC         = "maintenance/gc"
	ContextVMMethodMaintenancePressure   = "maintenance/pressure"

	MaintenanceCommandScan       = "maintenance.scan.request"
	MaintenanceCommandReport     = "maintenance.report.request"
	MaintenanceCommandQuarantine = "maintenance.quarantine.request"
	MaintenanceCommandRestore    = "maintenance.restore.request"
	MaintenanceCommandRelocate   = "maintenance.relocate.request"
	MaintenanceCommandPurge      = "maintenance.purge.request"
	MaintenanceCommandGC         = "maintenance.gc.request"
	MaintenanceCommandPressure   = "maintenance.pressure.request"
)

// MaintenanceCommandPublisher emits canonical maintenance/* intents to
// per-host maintenance drivers. Quarantine-not-delete semantics live in the
// driver; this publisher is the Bahia control-plane side (J4).
type MaintenanceCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewMaintenanceCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *MaintenanceCommandPublisher {
	return &MaintenanceCommandPublisher{publisher: publisher, signer: signer}
}

// MaintenanceCommand is a maintenance/* request for one worker.
type MaintenanceCommand struct {
	WorkerPubKey   string   `json:"worker_pubkey"`
	Paths          []string `json:"paths,omitempty"`
	Confirm        bool     `json:"confirm,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	AgentID        string   `json:"agent_id,omitempty"`
}

func (p *MaintenanceCommandPublisher) PublishScan(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	return p.publish(ctx, ContextVMMethodMaintenanceScan, MaintenanceCommandScan, "maintenance-scan", cmd)
}

func (p *MaintenanceCommandPublisher) PublishReport(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	return p.publish(ctx, ContextVMMethodMaintenanceReport, MaintenanceCommandReport, "maintenance-report", cmd)
}

func (p *MaintenanceCommandPublisher) PublishQuarantine(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	if len(cmd.Paths) == 0 {
		return nil, fmt.Errorf("maintenance/quarantine requires paths")
	}
	return p.publish(ctx, ContextVMMethodMaintenanceQuarantine, MaintenanceCommandQuarantine, "maintenance-quarantine", cmd)
}

func (p *MaintenanceCommandPublisher) PublishRestore(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	if len(cmd.Paths) == 0 {
		return nil, fmt.Errorf("maintenance/restore requires paths")
	}
	return p.publish(ctx, ContextVMMethodMaintenanceRestore, MaintenanceCommandRestore, "maintenance-restore", cmd)
}

func (p *MaintenanceCommandPublisher) PublishRelocate(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	if len(cmd.Paths) == 0 {
		return nil, fmt.Errorf("maintenance/relocate requires paths")
	}
	return p.publish(ctx, ContextVMMethodMaintenanceRelocate, MaintenanceCommandRelocate, "maintenance-relocate", cmd)
}

// PublishPurge is a Tier-2 action: the driver additionally demands
// confirm=true and (when configured) a Tier-2 method ACL match.
func (p *MaintenanceCommandPublisher) PublishPurge(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	if !cmd.Confirm {
		return nil, fmt.Errorf("maintenance/purge requires explicit confirm")
	}
	return p.publish(ctx, ContextVMMethodMaintenancePurge, MaintenanceCommandPurge, "maintenance-purge", cmd)
}

func (p *MaintenanceCommandPublisher) PublishGC(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	return p.publish(ctx, ContextVMMethodMaintenanceGC, MaintenanceCommandGC, "maintenance-gc", cmd)
}

func (p *MaintenanceCommandPublisher) PublishPressure(ctx context.Context, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	return p.publish(ctx, ContextVMMethodMaintenancePressure, MaintenanceCommandPressure, "maintenance-pressure", cmd)
}

func (p *MaintenanceCommandPublisher) publish(ctx context.Context, method, command, defaultPrefix string, cmd MaintenanceCommand) (*WorkerCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("maintenance command publisher is not configured")
	}
	workerPubKey := strings.TrimSpace(cmd.WorkerPubKey)
	if workerPubKey == "" {
		return nil, fmt.Errorf("worker_pubkey is required")
	}
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = defaultPrefix + ":" + uuid.NewString()
	}
	content := map[string]any{
		"reason": cmd.Reason,
	}
	if len(cmd.Paths) > 0 {
		content["paths"] = cmd.Paths
	}
	if cmd.Confirm {
		content["confirm"] = true
	}
	// The nonce stays inside the encrypted rumor. It prevents public audit/status
	// references to the rumor id from becoming a dictionary oracle for guessed
	// path payloads, including when a caller supplies a predictable idempotency key.
	tags := nostr.Tags{{"command", command}, {"worker", workerPubKey}, {"p", workerPubKey}, {"privacy-nonce", uuid.NewString()}}
	ev, published, dTag, err := publishContextVMCommandNIP59(ctx, p.publisher, p.signer, workerPubKey, method, dTag, cmd.AgentID, tags, content, "maintenance command")
	if err != nil {
		return nil, err
	}
	return &WorkerCommandReceipt{
		RequestEventID:  ev.ID.Hex(),
		RequestPubkey:   ev.PubKey.Hex(),
		RequestKind:     KindContextVMGiftWrap,
		ResultKind:      KindContextVMGiftWrap,
		StateKind:       KindCASControlState,
		DTag:            dTag,
		PublishedRelays: published,
		WorkerPubKey:    workerPubKey,
		Command:         command,
	}, nil
}
