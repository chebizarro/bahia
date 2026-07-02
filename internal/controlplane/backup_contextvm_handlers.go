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
	backupActionRepositoryRegister = "backup_repository_register"
	backupActionPolicyApply        = "backup_policy_apply"
	backupActionRecipeApply        = "backup_recipe_apply"
	backupActionDefinitionApply    = "backup_definition_apply"
	backupActionRun                = "backup_run"
	backupActionVerification       = "backup_verification"
	backupActionRestore            = "backup_restore"
	backupActionRetention          = "backup_retention"
	backupActionRestoreApproval    = "backup_restore_approval"
	backupActionRepositoryProbe    = "backup_repository_probe"

	backupDefaultRepositoryRegister = "backup-repository-register"
	backupDefaultPolicyApply        = "backup-policy-apply"
	backupDefaultRecipeApply        = "backup-recipe-apply"
	backupDefaultDefinitionApply    = "backup-definition-apply"
	backupDefaultRun                = "backup-run"
	backupDefaultVerification       = "backup-verification"
	backupDefaultRestore            = "backup-restore"
	backupDefaultRetention          = "backup-retention"
	backupDefaultRestoreApproval    = "backup-restore-approval"
	backupDefaultRepositoryProbe    = "backup-repository-probe"
)

// RegisterBackupAliasContextVMHandlers registers encrypted ContextVM method
// aliases used by the web UI while preserving the canonical backup action
// strings consumed by the backup control-plane handlers.
func RegisterBackupAliasContextVMHandlers(transport *EncryptedRequestTransport) {
	if transport == nil || transport.responder == nil {
		return
	}
	h := backupContextVMHandlers{publisher: transport.responder.publisher, signer: transport.responder.signer}
	transport.RegisterContextVMHandler(ContextVMMethodBackupRepositoryRegister, h.repositoryRegister)
	transport.RegisterContextVMHandler(ContextVMMethodBackupPolicyApply, h.policyApply)
	transport.RegisterContextVMHandler(ContextVMMethodBackupRecipeApply, h.recipeApply)
	transport.RegisterContextVMHandler(ContextVMMethodBackupDefinitionApply, h.definitionApply)
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, h.run)
	transport.RegisterContextVMHandler(ContextVMMethodBackupVerification, h.verification)
	transport.RegisterContextVMHandler(ContextVMMethodBackupRestore, h.restore)
	transport.RegisterContextVMHandler(ContextVMMethodBackupRetention, h.retention)
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
	RepositoryID    string `json:"repository_id,omitempty"`
	Repository      string `json:"repository,omitempty"`
	PolicyID        string `json:"policy_id,omitempty"`
	Policy          string `json:"policy,omitempty"`
	RecipeID        string `json:"recipe_id,omitempty"`
	Recipe          string `json:"recipe,omitempty"`
	DefinitionID    string `json:"definition_id,omitempty"`
	Definition      string `json:"definition,omitempty"`
	BackupRunID     string `json:"backup_run_id,omitempty"`
	RestoreID       string `json:"restore_id,omitempty"`
	Decision        string `json:"decision,omitempty"`
}

type backupRestoreApprovalContextVMPayload struct {
	RestoreID      string         `json:"restore_id"`
	Approved       *bool          `json:"approved,omitempty"`
	Decision       string         `json:"decision,omitempty"`
	Message        string         `json:"message,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	Reason         any            `json:"reason,omitempty"`
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

func (h backupContextVMHandlers) repositoryRegister(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRepositoryRegister,
		statusKind: KindBackupRunStatus,
		resultKind: KindBackupRepositoryRegisterResult,
		action:     backupActionRepositoryRegister,
		defaultD:   backupDefaultRepositoryRegister,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"repository", backupStringParam(params, "name", "repository")},
			{"name", backupStringParam(params, "name")},
			{"backend", backupStringParam(params, "backend")},
			{"repository_uri", backupStringParam(params, "repository_uri", "uri")},
			{"repository_id", backupStringParam(params, "id", "repository_id")},
		},
	})
	if receipt != nil {
		receipt.RepositoryID = backupStringParam(params, "id", "repository_id")
		receipt.Repository = backupStringParam(params, "name", "repository")
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) policyApply(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupPolicyApply,
		statusKind: KindBackupRunStatus,
		resultKind: KindBackupPolicyApplyResult,
		action:     backupActionPolicyApply,
		defaultD:   backupDefaultPolicyApply,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"policy", backupStringParam(params, "name", "policy")},
			{"name", backupStringParam(params, "name")},
			{"policy_id", backupStringParam(params, "id", "policy_id")},
			{"verification", backupStringParam(params, "verification_mode")},
		},
	})
	if receipt != nil {
		receipt.PolicyID = backupStringParam(params, "id", "policy_id")
		receipt.Policy = backupStringParam(params, "name", "policy")
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) recipeApply(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	recipe := backupStringParam(params, "recipe")
	if recipe == "" {
		recipe = backupRecipeCoordLocal(backupStringParam(params, "name", "recipe_name"), backupStringParam(params, "version", "recipe_version"))
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRecipeApply,
		statusKind: KindBackupRunStatus,
		resultKind: KindBackupRecipeApplyResult,
		action:     backupActionRecipeApply,
		defaultD:   backupDefaultRecipeApply,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"recipe", recipe},
			{"recipe_id", backupStringParam(params, "id", "recipe_id")},
			{"recipe_name", backupStringParam(params, "name", "recipe_name")},
			{"recipe_version", backupStringParam(params, "version", "recipe_version")},
			{"repository_id", backupStringParam(params, "repository_id")},
			{"policy_id", backupStringParam(params, "policy_id")},
			{"backend", backupStringParam(params, "backend")},
			{"target", backupStringParam(params, "target_ref", "target")},
		},
	})
	if receipt != nil {
		receipt.RecipeID = backupStringParam(params, "id", "recipe_id")
		receipt.Recipe = recipe
		receipt.RepositoryID = backupStringParam(params, "repository_id")
		receipt.PolicyID = backupStringParam(params, "policy_id")
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) definitionApply(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupDefinitionApply,
		statusKind: KindBackupRunStatus,
		resultKind: KindBackupDefinitionApplyResult,
		action:     backupActionDefinitionApply,
		defaultD:   backupDefaultDefinitionApply,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"definition", backupStringParam(params, "name", "definition")},
			{"name", backupStringParam(params, "name")},
			{"definition_id", backupStringParam(params, "id", "definition_id")},
			{"repository_id", backupStringParam(params, "repository_id")},
			{"policy_id", backupStringParam(params, "policy_id")},
			{"recipe_id", backupStringParam(params, "recipe_id")},
		},
	})
	if receipt != nil {
		receipt.DefinitionID = backupStringParam(params, "id", "definition_id")
		receipt.Definition = backupStringParam(params, "name", "definition")
		receipt.RepositoryID = backupStringParam(params, "repository_id")
		receipt.PolicyID = backupStringParam(params, "policy_id")
		receipt.RecipeID = backupStringParam(params, "recipe_id")
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) run(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRunRequest,
		statusKind: KindBackupRunStatus,
		resultKind: KindBackupRunResult,
		action:     backupActionRun,
		defaultD:   backupDefaultRun,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"recipe", backupStringParam(params, "recipe")},
			{"recipe_id", backupStringParam(params, "recipe_id")},
		},
	})
	if receipt != nil {
		receipt.RecipeID = backupStringParam(params, "recipe_id")
		receipt.Recipe = backupStringParam(params, "recipe")
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) verification(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	runID := backupStringParam(params, "backup_run_id", "run_id")
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("backup_run_id must be a UUID")
	}
	params["backup_run_id"] = runID
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupVerificationRequest,
		statusKind: KindBackupVerificationStatus,
		resultKind: KindBackupVerificationResult,
		action:     backupActionVerification,
		defaultD:   backupDefaultVerification,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"backup_run_id", runID},
			{"run", runID},
			{"verification_mode", backupStringParam(params, "mode", "verification_mode")},
		},
	})
	if receipt != nil {
		receipt.BackupRunID = runID
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) restore(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	runID := backupStringParam(params, "backup_run_id", "run_id")
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("backup_run_id must be a UUID")
	}
	params["backup_run_id"] = runID
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRestoreRequest,
		statusKind: KindBackupRestoreStatus,
		resultKind: KindBackupRestoreResult,
		action:     backupActionRestore,
		defaultD:   backupDefaultRestore,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"backup_run_id", runID},
			{"run", runID},
			{"target", backupStringParam(params, "restore_target_ref", "target")},
		},
	})
	if receipt != nil {
		receipt.BackupRunID = runID
	}
	return backupCommandAck(receipt), err
}

func (h backupContextVMHandlers) retention(ctx context.Context, request ContextVMRequest) (any, error) {
	params, err := backupContextVMParams(request)
	if err != nil {
		return nil, err
	}
	repositoryID := backupStringParam(params, "repository_id")
	if _, err := uuid.Parse(repositoryID); err != nil {
		return nil, fmt.Errorf("repository_id must be a UUID")
	}
	policyID := backupStringParam(params, "policy_id")
	if _, err := uuid.Parse(policyID); err != nil {
		return nil, fmt.Errorf("policy_id must be a UUID")
	}
	receipt, err := h.publish(ctx, backupPublishSpecLocal{
		kind:       KindBackupRetentionEnforce,
		statusKind: KindBackupObservation,
		resultKind: KindBackupRetentionResult,
		action:     backupActionRetention,
		defaultD:   backupDefaultRetention,
		dTag:       backupStringParam(params, "idempotency_key"),
		agentID:    backupStringParam(params, "agent_id"),
		content:    params,
		tags: nostr.Tags{
			{"repository_id", repositoryID},
			{"policy_id", policyID},
			{"dry_run", backupStringParam(params, "dry_run")},
		},
	})
	if receipt != nil {
		receipt.RepositoryID = repositoryID
		receipt.PolicyID = policyID
	}
	return backupCommandAck(receipt), err
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

func backupContextVMParams(request ContextVMRequest) (map[string]any, error) {
	params := map[string]any{}
	if err := decodeContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]any{}
	}
	return params, nil
}

func backupStringParam(params map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := params[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func backupRecipeCoordLocal(name, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || version == "" {
		return ""
	}
	return "recipe:" + name + ":" + version
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
