package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// NostrBackupCommandPublisher signs and publishes backup control-plane request events.
type NostrBackupCommandPublisher struct {
	publisher controlplane.NostrEventPublisher
	signer    canonicalnostr.Signer
}

// NewBackupCommandPublisher creates the signer-first publisher used by production MCP backup tools.
func NewBackupCommandPublisher(publisher controlplane.NostrEventPublisher, signer canonicalnostr.Signer) *NostrBackupCommandPublisher {
	return &NostrBackupCommandPublisher{publisher: publisher, signer: signer}
}

func (p *NostrBackupCommandPublisher) PublishBackupRepositoryRegisterRequest(ctx context.Context, cmd BackupRepositoryApplyCommand) (*BackupCommandReceipt, error) {
	content, err := backupCommandContent(cmd.Repository)
	if err != nil {
		return nil, err
	}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRepositoryRegister,
		resultKind: controlplane.KindBackupRepositoryRegisterResult,
		statusKind: controlplane.KindBackupRunStatus,
		action:     "backup_repository_register",
		defaultD:   "backup-repository-register",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"repository", cmd.Repository.Name},
			{"name", cmd.Repository.Name},
			{"backend", string(cmd.Repository.Backend)},
			{"repository_uri", cmd.Repository.RepositoryURI},
			{"repository_id", uuidTag(cmd.Repository.ID)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RepositoryID = uuidTag(cmd.Repository.ID)
			receipt.RepositoryName = cmd.Repository.Name
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupPolicyApplyRequest(ctx context.Context, cmd BackupPolicyApplyCommand) (*BackupCommandReceipt, error) {
	content, err := backupCommandContent(cmd.Policy)
	if err != nil {
		return nil, err
	}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupPolicyApply,
		resultKind: controlplane.KindBackupPolicyApplyResult,
		statusKind: controlplane.KindBackupRunStatus,
		action:     "backup_policy_apply",
		defaultD:   "backup-policy-apply",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"policy", cmd.Policy.Name},
			{"name", cmd.Policy.Name},
			{"policy_id", uuidTag(cmd.Policy.ID)},
			{"verification", string(cmd.Policy.VerificationMode)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.PolicyID = uuidTag(cmd.Policy.ID)
			receipt.PolicyName = cmd.Policy.Name
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRecipeApplyRequest(ctx context.Context, cmd BackupRecipeApplyCommand) (*BackupCommandReceipt, error) {
	content, err := backupCommandContent(cmd.Recipe)
	if err != nil {
		return nil, err
	}
	policyID := ""
	if cmd.Recipe.PolicyID != nil {
		policyID = uuidTag(*cmd.Recipe.PolicyID)
	}
	coord := backupRecipeCoord(cmd.Recipe.Name, cmd.Recipe.Version)
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRecipeApply,
		resultKind: controlplane.KindBackupRecipeApplyResult,
		statusKind: controlplane.KindBackupRunStatus,
		action:     "backup_recipe_apply",
		defaultD:   "backup-recipe-apply",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"recipe", coord},
			{"recipe_id", uuidTag(cmd.Recipe.ID)},
			{"recipe_name", cmd.Recipe.Name},
			{"recipe_version", cmd.Recipe.Version},
			{"repository_id", uuidTag(cmd.Recipe.RepositoryID)},
			{"policy_id", policyID},
			{"backend", string(cmd.Recipe.Backend)},
			{"target", cmd.Recipe.TargetRef},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RecipeID = uuidTag(cmd.Recipe.ID)
			receipt.RecipeName = cmd.Recipe.Name
			receipt.RecipeVersion = cmd.Recipe.Version
			receipt.RepositoryID = uuidTag(cmd.Recipe.RepositoryID)
			receipt.PolicyID = policyID
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupDefinitionApplyRequest(ctx context.Context, cmd BackupDefinitionApplyCommand) (*BackupCommandReceipt, error) {
	content, err := backupCommandContent(cmd.Definition)
	if err != nil {
		return nil, err
	}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupDefinitionApply,
		resultKind: controlplane.KindBackupDefinitionApplyResult,
		statusKind: controlplane.KindBackupRunStatus,
		action:     "backup_definition_apply",
		defaultD:   "backup-definition-apply",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"definition", cmd.Definition.Name},
			{"name", cmd.Definition.Name},
			{"definition_id", uuidTag(cmd.Definition.ID)},
			{"repository_id", uuidTag(cmd.Definition.RepositoryID)},
			{"policy_id", uuidTag(cmd.Definition.PolicyID)},
			{"recipe_id", uuidTag(cmd.Definition.RecipeID)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.DefinitionID = uuidTag(cmd.Definition.ID)
			receipt.DefinitionName = cmd.Definition.Name
			receipt.RepositoryID = uuidTag(cmd.Definition.RepositoryID)
			receipt.PolicyID = uuidTag(cmd.Definition.PolicyID)
			receipt.RecipeID = uuidTag(cmd.Definition.RecipeID)
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRepositoryProbeRequest(ctx context.Context, cmd BackupRepositoryProbeCommand) (*BackupCommandReceipt, error) {
	content := map[string]any{"repository": strings.TrimSpace(cmd.Repository), "metadata": cmd.Metadata}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRepositoryProbe,
		resultKind: controlplane.KindBackupRepositoryProbeResult,
		statusKind: controlplane.KindBackupObservation,
		action:     "backup_repository_probe",
		defaultD:   "backup-repository-probe",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"repository", cmd.Repository},
			{"repository_id", uuidTag(cmd.RepositoryID)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RepositoryID = uuidTag(cmd.RepositoryID)
			receipt.RepositoryName = cmd.Repository
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRunRequest(ctx context.Context, cmd BackupRunCommand) (*BackupCommandReceipt, error) {
	content := map[string]any{"recipe": strings.TrimSpace(cmd.Recipe), "metadata": cmd.Metadata}
	if cmd.RecipeID != uuid.Nil {
		content["recipe_id"] = cmd.RecipeID.String()
	}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRunRequest,
		resultKind: controlplane.KindBackupRunResult,
		statusKind: controlplane.KindBackupRunStatus,
		action:     "backup_run",
		defaultD:   "backup-run",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"recipe", cmd.Recipe},
			{"recipe_id", uuidTag(cmd.RecipeID)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RecipeID = uuidTag(cmd.RecipeID)
			receipt.RecipeName = cmd.Recipe
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupVerificationRequest(ctx context.Context, cmd BackupVerificationCommand) (*BackupCommandReceipt, error) {
	content := map[string]any{"backup_run_id": cmd.BackupRunID.String(), "mode": cmd.Mode, "metadata": cmd.Metadata}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupVerificationRequest,
		resultKind: controlplane.KindBackupVerificationResult,
		statusKind: controlplane.KindBackupVerificationStatus,
		action:     "backup_verification",
		defaultD:   "backup-verification",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"backup_run_id", uuidTag(cmd.BackupRunID)},
			{"run", uuidTag(cmd.BackupRunID)},
			{"verification_mode", string(cmd.Mode)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.BackupRunID = uuidTag(cmd.BackupRunID)
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRestoreRequest(ctx context.Context, cmd BackupRestoreCommand) (*BackupCommandReceipt, error) {
	content := map[string]any{"backup_run_id": cmd.BackupRunID.String(), "restore_target_ref": strings.TrimSpace(cmd.RestoreTargetRef), "metadata": cmd.Metadata}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRestoreRequest,
		resultKind: controlplane.KindBackupRestoreResult,
		statusKind: controlplane.KindBackupRestoreStatus,
		action:     "backup_restore",
		defaultD:   "backup-restore",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"backup_run_id", uuidTag(cmd.BackupRunID)},
			{"run", uuidTag(cmd.BackupRunID)},
			{"target", cmd.RestoreTargetRef},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.BackupRunID = uuidTag(cmd.BackupRunID)
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRestoreApprovalRequest(ctx context.Context, cmd BackupRestoreApprovalCommand) (*BackupCommandReceipt, error) {
	decision := "rejected"
	if cmd.Approved {
		decision = "approved"
	}
	content := map[string]any{"restore_id": cmd.RestoreID.String(), "approved": cmd.Approved, "decision": decision, "message": cmd.Message, "reason_code": cmd.ReasonCode, "reason": cmd.Reason}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRestoreApproval,
		resultKind: controlplane.KindBackupRestoreApprovalResult,
		statusKind: controlplane.KindBackupRestoreStatus,
		action:     "backup_restore_approval",
		defaultD:   "backup-restore-approval",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"restore_id", uuidTag(cmd.RestoreID)},
			{"restore", uuidTag(cmd.RestoreID)},
			{"decision", decision},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RestoreID = uuidTag(cmd.RestoreID)
			receipt.Decision = decision
		},
	})
}

func (p *NostrBackupCommandPublisher) PublishBackupRetentionRequest(ctx context.Context, cmd BackupRetentionCommand) (*BackupCommandReceipt, error) {
	content := map[string]any{"repository_id": cmd.RepositoryID.String(), "policy_id": cmd.PolicyID.String(), "dry_run": cmd.DryRun, "metadata": cmd.Metadata}
	return p.publish(ctx, backupPublishSpec{
		kind:       controlplane.KindBackupRetentionEnforce,
		resultKind: controlplane.KindBackupRetentionResult,
		statusKind: controlplane.KindBackupObservation,
		action:     "backup_retention",
		defaultD:   "backup-retention",
		dTag:       cmd.IdempotencyKey,
		agentID:    cmd.AgentID,
		content:    content,
		tags: nostr.Tags{
			{"repository_id", uuidTag(cmd.RepositoryID)},
			{"policy_id", uuidTag(cmd.PolicyID)},
			{"dry_run", fmt.Sprintf("%t", cmd.DryRun)},
		},
		receipt: func(receipt *BackupCommandReceipt) {
			receipt.RepositoryID = uuidTag(cmd.RepositoryID)
			receipt.PolicyID = uuidTag(cmd.PolicyID)
		},
	})
}

type backupPublishSpec struct {
	kind       int
	resultKind int
	statusKind int
	action     string
	defaultD   string
	dTag       string
	agentID    string
	content    map[string]any
	tags       nostr.Tags
	receipt    func(*BackupCommandReceipt)
}

func (p *NostrBackupCommandPublisher) publish(ctx context.Context, spec backupPublishSpec) (*BackupCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("backup command publisher is not configured")
	}
	dTag := strings.TrimSpace(spec.dTag)
	if dTag == "" {
		dTag = spec.defaultD + ":" + uuid.NewString()
	}
	content := backupCopyContent(spec.content)
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
	ev := &nostr.Event{Kind: nostr.Kind(spec.kind), CreatedAt: nostr.Now(), Tags: backupCompactTags(tags), Content: string(body)}
	if err := controlplane.SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign backup command event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		return nil, fmt.Errorf("publish backup command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish backup command event: no relay accepted the request")
	}
	receipt := &BackupCommandReceipt{
		RequestEventID:  ev.ID.Hex(),
		RequestPubkey:   ev.PubKey.Hex(),
		RequestKind:     spec.kind,
		StatusKind:      spec.statusKind,
		ResultKind:      spec.resultKind,
		ReadModelKinds:  backupReadModelKinds(),
		DTag:            dTag,
		PublishedRelays: published,
		Action:          spec.action,
	}
	if spec.receipt != nil {
		spec.receipt(receipt)
	}
	return receipt, nil
}

func backupCommandContent(value any) (map[string]any, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal backup command payload: %w", err)
	}
	content := map[string]any{}
	if err := json.Unmarshal(body, &content); err != nil {
		return nil, fmt.Errorf("normalize backup command payload: %w", err)
	}
	return content, nil
}

func backupCopyContent(content map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range content {
		if strings.TrimSpace(k) != "" && v != nil {
			out[k] = v
		}
	}
	return out
}

func backupCompactTags(tags nostr.Tags) nostr.Tags {
	out := make(nostr.Tags, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if len(tag) < 2 || strings.TrimSpace(tag[0]) == "" || strings.TrimSpace(tag[1]) == "" {
			continue
		}
		key := tag[0] + "\x00" + tag[1]
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func uuidTag(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func backupRecipeCoord(name, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || version == "" {
		return ""
	}
	return "recipe:" + name + ":" + version
}

var _ BackupCommandPublisher = (*NostrBackupCommandPublisher)(nil)
