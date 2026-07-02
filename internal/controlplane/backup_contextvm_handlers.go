package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
)

const (
	backupActionRestoreApproval  = "backup_restore_approval"
	backupActionRepositoryProbe  = "backup_repository_probe"
	backupDefaultRestoreApproval = "backup-restore-approval"
	backupDefaultRepositoryProbe = "backup-repository-probe"
)

// RegisterBackupAliasContextVMHandlers registers encrypted ContextVM method
// aliases used by the web UI while preserving the canonical backup action
// strings consumed by the backup control-plane handlers.
func RegisterBackupAliasContextVMHandlers(transport *EncryptedRequestTransport) {
	if transport == nil || transport.responder == nil {
		return
	}
	h := backupContextVMHandlers{publisher: transport.responder.publisher, signer: transport.responder.signer}
	transport.RegisterContextVMHandler(ContextVMMethodBackupRestoreApprovalAlias, h.restoreApproval)
	transport.RegisterContextVMHandler(ContextVMMethodBackupRepositoryProbe, h.repositoryProbe)
}

type backupContextVMHandlers struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

type backupContextVMReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	DTag            string `json:"d_tag"`
	PublishedRelays int    `json:"published_relays"`
	Action          string `json:"action"`
	RestoreID       string `json:"restore_id,omitempty"`
	RepositoryID    string `json:"repository_id,omitempty"`
	Repository      string `json:"repository,omitempty"`
	Decision        string `json:"decision,omitempty"`
}

type backupRestoreApprovalContextVMPayload struct {
	RestoreID      string         `json:"restore_id"`
	Approved       *bool          `json:"approved,omitempty"`
	Decision       string         `json:"decision,omitempty"`
	Message        string         `json:"message,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type backupRepositoryProbeContextVMPayload struct {
	RepositoryID   string         `json:"repository_id,omitempty"`
	Repository     string         `json:"repository,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

func (h backupContextVMHandlers) restoreApproval(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload backupRestoreApprovalContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	restoreID, err := uuid.Parse(strings.TrimSpace(payload.RestoreID))
	if err != nil {
		return nil, fmt.Errorf("restore_id must be a UUID")
	}
	approved, decision, err := normalizeBackupApprovalDecision(payload.Approved, payload.Decision)
	if err != nil {
		return nil, err
	}
	content := map[string]any{
		"restore_id":  restoreID.String(),
		"approved":    approved,
		"decision":    decision,
		"message":     payload.Message,
		"reason_code": payload.ReasonCode,
		"reason":      payload.Reason,
		"metadata":    payload.Metadata,
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRestoreApproval,
		statusKind: KindBackupRestoreStatus,
		resultKind: KindBackupRestoreApprovalResult,
		action:     backupActionRestoreApproval,
		defaultD:   backupDefaultRestoreApproval,
		dTag:       payload.IdempotencyKey,
		agentID:    payload.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"restore_id", restoreID.String()},
			{"restore", restoreID.String()},
			{"decision", decision},
		},
	})
	if receipt != nil {
		receipt.RestoreID = restoreID.String()
		receipt.Decision = decision
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) repositoryProbe(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload backupRepositoryProbeContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	repositoryID := strings.TrimSpace(payload.RepositoryID)
	repository := strings.TrimSpace(payload.Repository)
	if repositoryID == "" && repository == "" {
		return nil, fmt.Errorf("repository_id or repository is required")
	}
	if repositoryID != "" {
		if _, err := uuid.Parse(repositoryID); err != nil {
			return nil, fmt.Errorf("repository_id must be a UUID")
		}
	}
	content := map[string]any{"repository_id": repositoryID, "repository": repository, "metadata": payload.Metadata}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRepositoryProbe,
		statusKind: KindBackupObservation,
		resultKind: KindBackupRepositoryProbeResult,
		action:     backupActionRepositoryProbe,
		defaultD:   backupDefaultRepositoryProbe,
		dTag:       payload.IdempotencyKey,
		agentID:    payload.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"repository", repository},
			{"repository_id", repositoryID},
		},
	})
	if receipt != nil {
		receipt.RepositoryID = repositoryID
		receipt.Repository = repository
	}
	return backupCommandAck(receipt), err
}

type backupPublishSpecLocal struct {
	kind       int
	statusKind int
	resultKind int
	action     string
	defaultD   string
	dTag       string
	agentID    string
	content    map[string]any
	tags       nostr.Tags
}

func (h backupContextVMHandlers) publish(ctx context.Context, spec backupPublishSpecLocal) (*backupContextVMReceipt, error) {
	if h.publisher == nil {
		return nil, fmt.Errorf("backup command publisher is not configured")
	}
	dTag := strings.TrimSpace(spec.dTag)
	if dTag == "" {
		dTag = spec.defaultD + ":" + uuid.NewString()
	}
	content := cloneBackupContextVMContent(spec.content)
	content["idempotency_key"] = dTag
	content["action"] = spec.action
	if agentID := strings.TrimSpace(spec.agentID); agentID != "" {
		content["agent_id"] = agentID
	}
	body, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal backup command content: %w", err)
	}
	tags := nostr.Tags{{"d", dTag}, {"command", spec.action}}
	if agentID := strings.TrimSpace(spec.agentID); agentID != "" {
		tags = append(tags, nostr.Tag{"agent", agentID})
	}
	tags = append(tags, spec.tags...)
	event := &nostr.Event{Kind: nostr.Kind(spec.kind), CreatedAt: nostr.Now(), Tags: compactTags(tags), Content: string(body)}
	if err := SignGoNostrEvent(ctx, h.signer, event); err != nil {
		return nil, fmt.Errorf("sign backup command event: %w", err)
	}
	published, err := h.publisher.Publish(ctx, *event)
	if err != nil {
		return nil, fmt.Errorf("publish backup command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish backup command event: no relay accepted the request")
	}
	return &backupContextVMReceipt{RequestEventID: event.ID.Hex(), RequestPubkey: event.PubKey.Hex(), RequestKind: spec.kind, StatusKind: spec.statusKind, ResultKind: spec.resultKind, DTag: dTag, PublishedRelays: published, Action: spec.action}, nil
}

func normalizeBackupApprovalDecision(approved *bool, decision string) (bool, string, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if approved == nil {
		switch decision {
		case "approve", "approved":
			value := true
			approved = &value
		case "reject", "rejected", "deny", "denied":
			value := false
			approved = &value
		default:
			return false, "", fmt.Errorf("approved or decision is required")
		}
	}
	if *approved {
		return true, "approved", nil
	}
	return false, "rejected", nil
}

func cloneBackupContextVMContent(content map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range content {
		if strings.TrimSpace(k) != "" && v != nil {
			out[k] = v
		}
	}
	return out
}

func backupCommandAck(receipt *backupContextVMReceipt) map[string]any {
	if receipt == nil {
		return map[string]any{"status": "submitted"}
	}
	return map[string]any{"status": "submitted", "receipt": receipt, "request_event_id": receipt.RequestEventID, "d_tag": receipt.DTag, "action": receipt.Action}
}
