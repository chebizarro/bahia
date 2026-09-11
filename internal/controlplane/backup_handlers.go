package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
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
		RequestedBy:        backupRequestActor(event),
		RequestEventID:     event.ID.Hex(),
		RequestKind:        int(event.Kind),
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
	return r.authorizeBackupCommandRequest(ctx, event, step, KindBackupRunResult)
}

func (r *Reactor) authorizeBackupCommandRequest(ctx context.Context, event *nostr.Event, step string, resultKind int) bool {
	if !r.isAuthorized(event.PubKey.Hex()) {
		_ = r.publishBackupCommandFailure(ctx, event, resultKind, "rejected", "unauthorized", "requester not in authorized list")
		return false
	}
	if tagValueNostr(event.Tags, "d") == "" {
		_ = r.publishBackupCommandFailure(ctx, event, resultKind, "failed", "validation_error", "d tag is required for addressable backup command events")
		return false
	}
	authority, delegated, err := backupRequestAuthorityFromEvent(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, resultKind, "rejected", "invalid_delegation", err.Error())
		return false
	}
	if delegated {
		if r.signer == nil {
			_ = r.publishBackupCommandFailure(ctx, event, resultKind, "rejected", "invalid_delegation", "backup delegation issuer is not configured")
			return false
		}
		issuer, err := r.signer.GetPublicKey(ctx)
		if err != nil || normalizeEncryptedPubkey(issuer.Hex()) != authority.ServicePubkey {
			_ = r.publishBackupCommandFailure(ctx, event, resultKind, "rejected", "invalid_delegation", "backup delegation issuer does not match the configured service signer")
			return false
		}
	}
	if r.backupRegistry == nil {
		_ = r.publishBackupCommandFailure(ctx, event, resultKind, "failed", step+"_unavailable", "backup registry is not configured")
		return false
	}
	return true
}

func backupRequestAuthorityFromEvent(event *nostr.Event) (backupDelegationRecord, bool, error) {
	if event == nil {
		return backupDelegationRecord{}, false, fmt.Errorf("backup command event is required")
	}
	servicePubkey := normalizeEncryptedPubkey(event.PubKey.Hex())
	version, err := singleBackupDelegationTag(event.Tags, "delegation", false)
	if err != nil {
		return backupDelegationRecord{}, true, err
	}
	if version == "" {
		return backupDelegationRecord{
			RequesterPubkey:  servicePubkey,
			RequestEventID:   event.ID.Hex(),
			RequestEventKind: int(event.Kind),
			ServicePubkey:    servicePubkey,
		}, false, nil
	}
	if version != backupDelegationVersion {
		return backupDelegationRecord{}, true, fmt.Errorf("unsupported backup delegation version")
	}
	var content struct {
		RequestAuthority *backupDelegationRecord `json:"request_authority"`
	}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return backupDelegationRecord{}, true, fmt.Errorf("decode backup delegation record: %w", err)
	}
	if content.RequestAuthority == nil {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation record is required")
	}
	record := *content.RequestAuthority
	if record.Version != backupDelegationVersion || record.Version != version {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation version mismatch")
	}
	if !validBackupHexIdentity(record.RequesterPubkey) || !validBackupHexIdentity(record.ServicePubkey) || !validBackupHexIdentity(record.RequestEventID) {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation identities must be 32-byte lowercase hex values")
	}
	if record.ServicePubkey != servicePubkey {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation service identity does not match event signer")
	}
	if record.RequesterPubkey == record.ServicePubkey {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation requester must differ from service signer")
	}
	if record.RequestEventKind != int(KindContextVMMessage) {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation request kind is invalid")
	}
	if _, err := uuid.Parse(record.TenantID); err != nil {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation tenant is invalid")
	}
	if record.Capability != string(domain.PermManageBackups) {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation capability is invalid")
	}
	requesterTag, requesterErr := singleBackupDelegationTag(event.Tags, "requester", true)
	requestEventTag, requestEventErr := singleBackupDelegationTag(event.Tags, "request_event", true)
	requestKindTag, requestKindErr := singleBackupDelegationTag(event.Tags, "request_kind", true)
	tenantTag, tenantErr := singleBackupDelegationTag(event.Tags, "tenant", true)
	capabilityTag, capabilityErr := singleBackupDelegationTag(event.Tags, "capability", true)
	if requesterErr != nil || requestEventErr != nil || requestKindErr != nil || tenantErr != nil || capabilityErr != nil {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation requires one value for every authority tag")
	}
	requestKind, err := strconv.Atoi(requestKindTag)
	if err != nil || requestKind != record.RequestEventKind ||
		requesterTag != record.RequesterPubkey ||
		requestEventTag != record.RequestEventID ||
		tenantTag != record.TenantID ||
		capabilityTag != record.Capability {
		return backupDelegationRecord{}, true, fmt.Errorf("backup delegation tags do not match signed record")
	}
	return record, true, nil
}

func singleBackupDelegationTag(tags nostr.Tags, key string, required bool) (string, error) {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			count++
			value = strings.TrimSpace(tag[1])
		}
	}
	if count > 1 || (count == 1 && value == "") || (required && count != 1) {
		return "", fmt.Errorf("backup delegation tag %s must occur exactly once", key)
	}
	return value, nil
}

func validBackupHexIdentity(value string) bool {
	if value != strings.ToLower(value) || len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func backupRequestActor(event *nostr.Event) string {
	record, _, err := backupRequestAuthorityFromEvent(event)
	if err != nil {
		return ""
	}
	return record.RequesterPubkey
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
	metadata["nostr_event_id"] = event.ID.Hex()
	authority, delegated, err := backupRequestAuthorityFromEvent(event)
	if err == nil {
		metadata["nostr_request_pubkey"] = authority.RequesterPubkey
		metadata["nostr_delegated"] = delegated
		if delegated {
			metadata["nostr_request_event_id"] = authority.RequestEventID
			metadata["nostr_request_event_kind"] = authority.RequestEventKind
			metadata["nostr_service_pubkey"] = authority.ServicePubkey
			metadata["nostr_delegation_version"] = authority.Version
			metadata["nostr_tenant_id"] = authority.TenantID
			metadata["nostr_capability"] = authority.Capability
		}
	}
	metadata["nostr_request_kind"] = int(event.Kind)
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
	return r.publishBackupCommandFailure(ctx, requestEvent, KindBackupRunResult, status, code, message)
}

func (r *Reactor) publishBackupCommandFailure(ctx context.Context, requestEvent *nostr.Event, resultKind int, status, code, message string) error {
	requestEventID := requestEvent.ID.Hex()
	requestPubkey := requestEvent.PubKey.Hex()
	content := map[string]any{"request_event_id": requestEventID, "status": status, "message": message}
	if status == "failed" || status == "rejected" {
		content["error"] = map[string]any{"code": code, "message": message}
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{{"d", "result:" + requestEventID}, {"e", requestEventID, "", "reply"}, {"p", requestPubkey}, {"status", status}, {"result", code}}
	tags = appendBackupRequestTags(tags, requestEvent)
	event := &nostr.Event{Kind: nostr.Kind(resultKind), CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign backup result: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func appendBackupRequestTags(tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	allowed := map[string]struct{}{"recipe": {}, "recipe_id": {}, "recipe_name": {}, "recipe_version": {}, "run": {}, "backup_run_id": {}, "restore": {}, "restore_id": {}, "retention": {}, "retention_run_id": {}, "verification": {}, "verification_id": {}, "verification_mode": {}, "verification_status": {}, "definition": {}, "definition_id": {}, "policy": {}, "policy_id": {}, "repository": {}, "repository_id": {}, "repository_uri": {}, "target": {}, "backend": {}, "site": {}, "environment": {}, "worker": {}, "decision": {}}
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
