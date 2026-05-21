package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type backupRunRegistry interface {
	GetRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error)
	GetRecipeByNameVersion(ctx context.Context, name, version string) (*domain.BackupRecipe, error)
	GetRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error)
	GetPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error)
	CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error)
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

type backupRunRequest struct {
	RecipeID string         `json:"recipe_id,omitempty"`
	Recipe   string         `json:"recipe,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (r *Reactor) handleBackupRunRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupRequest(ctx, event, "backup_run") {
		return
	}
	if r.backupExecutor == nil {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "backup_coordinator_unavailable", "backup run coordinator is not configured")
		return
	}
	req, err := parseBackupRunRequest(event)
	if err != nil {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "parse_error", err.Error())
		return
	}
	recipe, err := r.resolveBackupRecipe(ctx, req.RecipeID, req.Recipe)
	if err != nil {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "recipe_resolution_error", err.Error())
		return
	}
	repositoryRecord, err := r.backupRegistry.GetRepository(ctx, recipe.RepositoryID)
	if err != nil || repositoryRecord == nil {
		if err == nil {
			err = fmt.Errorf("backup repository %s not found", recipe.RepositoryID)
		}
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "repository_resolution_error", err.Error())
		return
	}
	var policy *domain.BackupPolicy
	if recipe.PolicyID != nil {
		policy, err = r.backupRegistry.GetPolicy(ctx, *recipe.PolicyID)
		if err != nil || policy == nil {
			if err == nil {
				err = fmt.Errorf("backup policy %s not found", *recipe.PolicyID)
			}
			_ = r.publishBackupRequestFailure(ctx, event, "failed", "policy_resolution_error", err.Error())
			return
		}
	}
	run := &domain.BackupRun{
		ID:                 uuid.New(),
		RecipeID:           recipe.ID,
		RepositoryID:       recipe.RepositoryID,
		RequestedBy:        event.PubKey,
		RequestEventID:     event.ID,
		RequestKind:        event.Kind,
		RequestDTag:        tagValueNostr(event.Tags, "d"),
		Status:             domain.RunStatusQueued,
		Backend:            recipe.Backend,
		TargetRef:          recipe.TargetRef,
		VerificationStatus: domain.BackupVerificationPending,
		Metadata: backupNostrMetadata(event, req.Metadata, map[string]any{
			"nostr_request_command": "backup_run",
			"nostr_recipe_coord":    firstNonEmpty(req.Recipe, tagValueNostr(event.Tags, "recipe"), backupRecipeCoordinate(recipe)),
			"nostr_repository_name": repositoryRecord.Name,
			"nostr_repository_id":   repositoryRecord.ID.String(),
			"nostr_policy_name":     backupPolicyName(policy),
			"nostr_backend":         string(recipe.Backend),
			"nostr_target_ref":      recipe.TargetRef,
			"verification_required": policy != nil && policy.RequireVerification,
			"verification_mode":     backupVerificationMode(recipe, policy),
		}),
	}
	if recipe.PolicyID != nil {
		run.PolicyID = recipe.PolicyID
	}
	createdRun, created, err := r.backupRegistry.CreateBackupRunIfAbsent(ctx, run)
	if err != nil {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "run_create_error", err.Error())
		return
	}
	if r.backupResponder != nil {
		step := "queued"
		message := "backup run queued"
		if !created {
			step = "duplicate"
			message = "backup run request already accepted for this requester and d tag"
		}
		_ = r.backupResponder.PublishBackupRunStatus(ctx, createdRun, step, message)
	}
	if !created {
		if backupRunTerminal(createdRun) && r.backupResponder != nil {
			verification, _ := r.backupRegistry.GetBackupVerificationByRunID(ctx, createdRun.ID)
			_ = r.backupResponder.PublishBackupRunResult(ctx, createdRun, verification, "backup run already completed")
		}
		return
	}
	go func(runID uuid.UUID) {
		if err := r.backupExecutor.ProcessBackupRun(ctx, runID); err != nil {
			r.logger.Warn("backup run executor failed", "run_id", runID.String(), "error", err)
		}
	}(createdRun.ID)
}

func (r *Reactor) authorizeBackupRequest(ctx context.Context, event *nostr.Event, step string) bool {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishBackupRequestFailure(ctx, event, "rejected", "unauthorized", "requester not in authorized list")
		return false
	}
	if tagValueNostr(event.Tags, "d") == "" {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", "validation_error", "d tag is required for addressable backup command events")
		return false
	}
	if r.backupRegistry == nil {
		_ = r.publishBackupRequestFailure(ctx, event, "failed", step+"_unavailable", "backup registry is not configured")
		return false
	}
	return true
}

func parseBackupRunRequest(event *nostr.Event) (*backupRunRequest, error) {
	var req backupRunRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.Recipe == "" {
		req.Recipe = tagValueNostr(event.Tags, "recipe")
	}
	if req.RecipeID == "" {
		req.RecipeID = tagValueNostr(event.Tags, "recipe_id")
	}
	if req.RecipeID == "" && req.Recipe == "" {
		return nil, fmt.Errorf("recipe or recipe_id is required")
	}
	return &req, nil
}

func (r *Reactor) resolveBackupRecipe(ctx context.Context, recipeID, coord string) (*domain.BackupRecipe, error) {
	if recipeID != "" {
		id, err := uuid.Parse(recipeID)
		if err != nil {
			return nil, fmt.Errorf("invalid recipe_id: %w", err)
		}
		recipe, err := r.backupRegistry.GetRecipe(ctx, id)
		if err != nil {
			return nil, err
		}
		if recipe == nil {
			return nil, fmt.Errorf("backup recipe %s not found", id)
		}
		return recipe, nil
	}
	name, version, err := parseBackupRecipeCoordinate(coord)
	if err != nil {
		return nil, err
	}
	recipe, err := r.backupRegistry.GetRecipeByNameVersion(ctx, name, version)
	if err != nil {
		return nil, err
	}
	if recipe == nil {
		return nil, fmt.Errorf("backup recipe %q version %q not found", name, version)
	}
	return recipe, nil
}

func parseBackupRecipeCoordinate(coord string) (string, string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(coord), "recipe:")
	name, version, ok := strings.Cut(trimmed, ":")
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return "", "", fmt.Errorf("recipe coordinate must be recipe:<name>:<version>")
	}
	return strings.TrimSpace(name), strings.TrimSpace(version), nil
}

func backupNostrMetadata(event *nostr.Event, requestMetadata map[string]any, extra map[string]any) map[string]any {
	metadata := map[string]any{}
	for k, v := range requestMetadata {
		if v != nil {
			metadata[k] = v
		}
	}
	metadata["nostr_event_id"] = event.ID
	metadata["nostr_request_pubkey"] = event.PubKey
	metadata["nostr_request_kind"] = event.Kind
	metadata["nostr_d_tag"] = tagValueNostr(event.Tags, "d")
	for _, key := range []string{"recipe", "recipe_id", "repository", "repository_id", "policy", "policy_id", "target", "backend", "site", "environment", "worker", "verification"} {
		if value := tagValueNostr(event.Tags, key); value != "" {
			metadata["nostr_tag_"+key] = value
		}
	}
	for k, v := range extra {
		if v != nil && strings.TrimSpace(fmt.Sprint(v)) != "" {
			metadata[k] = v
		}
	}
	return metadata
}

func backupRecipeCoordinate(recipe *domain.BackupRecipe) string {
	if recipe == nil || recipe.Name == "" || recipe.Version == "" {
		return ""
	}
	return fmt.Sprintf("recipe:%s:%s", recipe.Name, recipe.Version)
}

func backupPolicyName(policy *domain.BackupPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.Name
}

func backupVerificationMode(recipe *domain.BackupRecipe, policy *domain.BackupPolicy) string {
	if policy != nil && policy.RequireVerification {
		return string(policy.VerificationMode)
	}
	if recipe != nil {
		return string(recipe.VerificationMode)
	}
	return ""
}

func backupRunTerminal(run *domain.BackupRun) bool {
	return run != nil && (run.Status == domain.RunStatusSucceeded || run.Status == domain.RunStatusFailed || run.Status == domain.RunStatusCancelled || run.Status == domain.RunStatusTimeout)
}

func (r *Reactor) publishBackupRequestFailure(ctx context.Context, requestEvent *nostr.Event, status, code, message string) error {
	content := map[string]any{"request_event_id": requestEvent.ID, "status": status, "message": message}
	if status == "failed" || status == "rejected" {
		content["error"] = map[string]any{"code": code, "message": message}
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{{"d", "result:" + requestEvent.ID}, {"e", requestEvent.ID, "", "reply"}, {"p", requestEvent.PubKey}, {"status", status}, {"result", code}}
	tags = appendBackupRequestTags(tags, requestEvent)
	event := &nostr.Event{Kind: KindBackupRunResult, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign backup result: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func appendBackupRequestTags(tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	allowed := map[string]struct{}{"recipe": {}, "recipe_id": {}, "run": {}, "policy": {}, "repository": {}, "repository_id": {}, "target": {}, "backend": {}, "site": {}, "environment": {}, "worker": {}, "verification": {}}
	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		if _, ok := allowed[tag[0]]; ok {
			tags = append(tags, nostr.Tag{tag[0], tag[1]})
		}
	}
	return tags
}
