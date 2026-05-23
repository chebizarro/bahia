package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type backupRegistryMutationRegistry interface {
	CreateOrUpdateRepository(ctx context.Context, repo *domain.BackupRepository) error
	CreateOrUpdatePolicy(ctx context.Context, policy *domain.BackupPolicy) error
	CreateOrUpdateRecipe(ctx context.Context, recipe *domain.BackupRecipe) error
	GetRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error)
	GetPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error)
}

type BackupDefinitionApplyRegistry interface {
	UpsertBackupDefinition(ctx context.Context, definition *domain.BackupDefinition) error
	GetBackupDefinitionByName(ctx context.Context, name string) (*domain.BackupDefinition, error)
}

type backupVerificationRegistry interface {
	GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error)
	RecordBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

type BackupVerificationControlPlaneExecutor interface {
	ProcessBackupVerification(ctx context.Context, verificationID uuid.UUID) error
}

type BackupRepositoryProbeControlPlaneExecutor interface {
	ProcessBackupRepositoryProbe(ctx context.Context, repositoryID uuid.UUID, requestEventID string) error
}

type backupRepositoryProbeRequest struct {
	RepositoryID string         `json:"repository_id,omitempty"`
	Repository   string         `json:"repository,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type backupVerificationRequest struct {
	BackupRunID string                        `json:"backup_run_id,omitempty"`
	Mode        domain.BackupVerificationMode `json:"mode,omitempty"`
	Metadata    map[string]any                `json:"metadata,omitempty"`
}

func (r *Reactor) handleBackupRepositoryRegisterRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_repository_register", KindBackupRepositoryRegisterResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRegistryMutationRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryRegisterResult, "failed", "backup_repository_register_unavailable", "backup repository registry is not configured")
		return
	}
	repo, err := parseBackupRepositoryRegisterRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryRegisterResult, "failed", "parse_error", err.Error())
		return
	}
	if existing, err := registry.GetRepositoryByName(ctx, repo.Name); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryRegisterResult, "failed", "repository_lookup_error", err.Error())
		return
	} else if existing != nil && repo.ID == uuid.Nil {
		repo.ID = existing.ID
	}
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	if repo.Metadata == nil {
		repo.Metadata = map[string]any{}
	}
	mergeMetadata(repo.Metadata, backupNostrMetadata(event, repo.Metadata, map[string]any{"nostr_request_command": "backup_repository_register"}))
	if err := domain.ValidateBackupRepository(repo); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryRegisterResult, "failed", "validation_error", err.Error())
		return
	}
	if err := registry.CreateOrUpdateRepository(ctx, repo); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryRegisterResult, "failed", "repository_apply_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupRepositoryRegisterResult, "backup_repository_register", "success", "backup repository registered", map[string]any{"repository_id": repo.ID.String(), "name": repo.Name, "backend": string(repo.Backend), "repository_uri": repo.RepositoryURI}, nostr.Tags{{"repository", repo.Name}, {"repository_id", repo.ID.String()}, {"backend", string(repo.Backend)}})
}

func (r *Reactor) handleBackupPolicyApplyRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_policy_apply", KindBackupPolicyApplyResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRegistryMutationRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupPolicyApplyResult, "failed", "backup_policy_apply_unavailable", "backup policy registry is not configured")
		return
	}
	policy, err := parseBackupPolicyApplyRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupPolicyApplyResult, "failed", "parse_error", err.Error())
		return
	}
	if existing, err := registry.GetPolicyByName(ctx, policy.Name); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupPolicyApplyResult, "failed", "policy_lookup_error", err.Error())
		return
	} else if existing != nil && policy.ID == uuid.Nil {
		policy.ID = existing.ID
	}
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	if policy.Metadata == nil {
		policy.Metadata = map[string]any{}
	}
	mergeMetadata(policy.Metadata, backupNostrMetadata(event, policy.Metadata, map[string]any{"nostr_request_command": "backup_policy_apply"}))
	if err := domain.ValidateBackupPolicy(policy); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupPolicyApplyResult, "failed", "validation_error", err.Error())
		return
	}
	if err := registry.CreateOrUpdatePolicy(ctx, policy); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupPolicyApplyResult, "failed", "policy_apply_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupPolicyApplyResult, "backup_policy_apply", "success", "backup policy applied", map[string]any{"policy_id": policy.ID.String(), "name": policy.Name, "require_verification": policy.RequireVerification, "verification_mode": string(policy.VerificationMode)}, nostr.Tags{{"policy", policy.Name}, {"policy_id", policy.ID.String()}, {"verification", string(policy.VerificationMode)}})
}

func (r *Reactor) handleBackupRecipeApplyRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_recipe_apply", KindBackupRecipeApplyResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRegistryMutationRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "backup_recipe_apply_unavailable", "backup recipe registry is not configured")
		return
	}
	recipe, err := parseBackupRecipeApplyRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "parse_error", err.Error())
		return
	}
	if recipe.ID == uuid.Nil {
		if existing, err := r.resolveBackupRecipe(ctx, "", backupRecipeCoordinate(recipe)); err == nil && existing != nil {
			recipe.ID = existing.ID
		}
	}
	if recipe.ID == uuid.Nil {
		recipe.ID = uuid.New()
	}
	if recipe.Metadata == nil {
		recipe.Metadata = map[string]any{}
	}
	mergeMetadata(recipe.Metadata, backupNostrMetadata(event, recipe.Metadata, map[string]any{"nostr_request_command": "backup_recipe_apply", "nostr_recipe_coord": backupRecipeCoordinate(recipe)}))
	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "validation_error", err.Error())
		return
	}
	repo, err := r.backupRegistry.GetRepository(ctx, recipe.RepositoryID)
	if err != nil || repo == nil {
		if err == nil {
			err = fmt.Errorf("backup repository %s not found", recipe.RepositoryID)
		}
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "repository_resolution_error", err.Error())
		return
	}
	if recipe.PolicyID != nil {
		policy, err := r.backupRegistry.GetPolicy(ctx, *recipe.PolicyID)
		if err != nil || policy == nil {
			if err == nil {
				err = fmt.Errorf("backup policy %s not found", *recipe.PolicyID)
			}
			_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "policy_resolution_error", err.Error())
			return
		}
	}
	if err := registry.CreateOrUpdateRecipe(ctx, recipe); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRecipeApplyResult, "failed", "recipe_apply_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupRecipeApplyResult, "backup_recipe_apply", "success", "backup recipe applied", map[string]any{"recipe_id": recipe.ID.String(), "name": recipe.Name, "version": recipe.Version, "repository_id": recipe.RepositoryID.String(), "backend": string(recipe.Backend), "target_ref": recipe.TargetRef}, nostr.Tags{{"recipe", backupRecipeCoordinate(recipe)}, {"recipe_id", recipe.ID.String()}, {"repository_id", recipe.RepositoryID.String()}, {"backend", string(recipe.Backend)}})
}

func (r *Reactor) handleBackupDefinitionApplyRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_definition_apply", KindBackupDefinitionApplyResult) {
		return
	}
	registry := r.backupDefinitionRegistry
	if registry == nil {
		if candidate, ok := any(r.backupRegistry).(BackupDefinitionApplyRegistry); ok {
			registry = candidate
		}
	}
	if registry == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "backup_definition_apply_unavailable", "backup definition registry is not configured")
		return
	}
	definition, err := parseBackupDefinitionApplyRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "parse_error", err.Error())
		return
	}
	if definition.ID == uuid.Nil {
		existing, err := registry.GetBackupDefinitionByName(ctx, definition.Name)
		if err != nil {
			_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "definition_lookup_error", err.Error())
			return
		}
		if existing != nil {
			definition.ID = existing.ID
		}
	}
	if definition.ID == uuid.Nil {
		definition.ID = uuid.New()
	}
	if definition.CreatedBy == "" {
		definition.CreatedBy = event.PubKey
	}
	repo, err := r.backupRegistry.GetRepository(ctx, definition.RepositoryID)
	if err != nil || repo == nil {
		if err == nil {
			err = fmt.Errorf("backup repository %s not found", definition.RepositoryID)
		}
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "repository_resolution_error", err.Error())
		return
	}
	policy, err := r.backupRegistry.GetPolicy(ctx, definition.PolicyID)
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("backup policy %s not found", definition.PolicyID)
		}
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "policy_resolution_error", err.Error())
		return
	}
	recipe, err := r.backupRegistry.GetRecipe(ctx, definition.RecipeID)
	if err != nil || recipe == nil {
		if err == nil {
			err = fmt.Errorf("backup recipe %s not found", definition.RecipeID)
		}
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "recipe_resolution_error", err.Error())
		return
	}
	definition.RepositoryName = repo.Name
	definition.PolicyName = policy.Name
	definition.RecipeName = recipe.Name
	definition.RecipeVersion = recipe.Version
	if definition.Metadata == nil {
		definition.Metadata = map[string]any{}
	}
	mergeMetadata(definition.Metadata, backupNostrMetadata(event, definition.Metadata, map[string]any{"nostr_request_command": "backup_definition_apply"}))
	if err := domain.ValidateBackupDefinition(definition); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "validation_error", err.Error())
		return
	}
	if err := registry.UpsertBackupDefinition(ctx, definition); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupDefinitionApplyResult, "failed", "definition_apply_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupDefinitionApplyResult, "backup_definition_apply", "success", "backup definition applied", map[string]any{"definition_id": definition.ID.String(), "name": definition.Name, "repository_id": definition.RepositoryID.String(), "policy_id": definition.PolicyID.String(), "recipe_id": definition.RecipeID.String(), "schedule_enabled": definition.ScheduleEnabled}, nostr.Tags{{"definition", definition.Name}, {"definition_id", definition.ID.String()}, {"repository_id", definition.RepositoryID.String()}, {"policy_id", definition.PolicyID.String()}, {"recipe_id", definition.RecipeID.String()}})
}

func (r *Reactor) handleBackupRepositoryProbeRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_repository_probe", KindBackupRepositoryProbeResult) {
		return
	}
	if r.backupRepositoryProbeExecutor == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryProbeResult, "failed", "backup_repository_probe_unavailable", "backup repository probe executor is not configured")
		return
	}
	req, err := parseBackupRepositoryProbeRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryProbeResult, "failed", "parse_error", err.Error())
		return
	}
	repo, err := r.resolveBackupRepository(ctx, req.RepositoryID, req.Repository)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRepositoryProbeResult, "failed", "repository_resolution_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupRepositoryProbeResult, "backup_repository_probe", "queued", "backup repository probe queued", map[string]any{"repository_id": repo.ID.String(), "name": repo.Name, "backend": string(repo.Backend)}, nostr.Tags{{"repository", repo.Name}, {"repository_id", repo.ID.String()}, {"backend", string(repo.Backend)}})
	go func(repositoryID uuid.UUID, requestEventID string) {
		if err := r.backupRepositoryProbeExecutor.ProcessBackupRepositoryProbe(ctx, repositoryID, requestEventID); err != nil {
			r.logger.Warn("backup repository probe executor failed", "repository_id", repositoryID.String(), "error", err)
		}
	}(repo.ID, event.ID)
}

func (r *Reactor) handleBackupVerificationRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_verification", KindBackupVerificationResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupVerificationRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "backup_verification_unavailable", "backup verification registry is not configured")
		return
	}
	if r.backupVerificationExecutor == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "backup_verification_executor_unavailable", "backup verification executor is not configured")
		return
	}
	req, err := parseBackupVerificationRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "parse_error", err.Error())
		return
	}
	runID, err := uuid.Parse(req.BackupRunID)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "validation_error", "backup_run_id must be a UUID")
		return
	}
	run, err := registry.GetBackupRun(ctx, runID)
	if err != nil || run == nil {
		if err == nil {
			err = fmt.Errorf("backup run %s not found", runID)
		}
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "backup_run_resolution_error", err.Error())
		return
	}
	if !run.SnapshotCreated || strings.TrimSpace(run.SnapshotID) == "" {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "validation_error", "backup run must have a snapshot before verification can be requested")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = run.VerificationMode
	}
	if mode == "" || mode == domain.BackupVerificationNone || !mode.IsValid() {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "validation_error", "verification mode must be a supported non-none mode")
		return
	}
	if existing, err := registry.GetBackupVerificationByRunID(ctx, runID); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "verification_lookup_error", err.Error())
		return
	} else if existing != nil {
		status := "duplicate"
		message := "backup verification is already recorded"
		if existing.Status == domain.BackupVerificationPending {
			message = "backup verification is already pending"
		}
		_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupVerificationResult, "backup_verification", status, message, map[string]any{"verification_id": existing.ID.String(), "backup_run_id": run.ID.String(), "mode": string(existing.Mode), "verification_status": string(existing.Status)}, nostr.Tags{{"run", run.ID.String()}, {"backup_run_id", run.ID.String()}, {"verification_id", existing.ID.String()}, {"verification_status", string(existing.Status)}})
		return
	}
	record := &domain.BackupVerificationRecord{ID: uuid.New(), BackupRunID: run.ID, Mode: mode, Status: domain.BackupVerificationPending, Verified: false, Evidence: backupNostrMetadata(event, req.Metadata, map[string]any{"nostr_request_command": "backup_verification", "nostr_backup_run_id": run.ID.String(), "nostr_snapshot_id": run.SnapshotID})}
	if err := registry.RecordBackupVerification(ctx, record); err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupVerificationResult, "failed", "verification_record_error", err.Error())
		return
	}
	_ = r.publishBackupRegistryMutationResult(ctx, event, KindBackupVerificationResult, "backup_verification", "queued", "backup verification queued", map[string]any{"verification_id": record.ID.String(), "backup_run_id": run.ID.String(), "mode": string(record.Mode), "verification_status": string(record.Status)}, nostr.Tags{{"run", run.ID.String()}, {"backup_run_id", run.ID.String()}, {"verification_id", record.ID.String()}, {"verification_status", string(record.Status)}})
	go func(verificationID uuid.UUID) {
		if err := r.backupVerificationExecutor.ProcessBackupVerification(ctx, verificationID); err != nil {
			r.logger.Warn("backup verification executor failed", "verification_id", verificationID.String(), "error", err)
		}
	}(record.ID)
}

func parseBackupRepositoryRegisterRequest(event *nostr.Event) (*domain.BackupRepository, error) {
	var repo domain.BackupRepository
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &repo); err != nil {
			return nil, err
		}
	}
	if repo.Name == "" {
		repo.Name = firstNonEmpty(tagValueNostr(event.Tags, "repository"), tagValueNostr(event.Tags, "name"))
	}
	if repo.Backend == "" {
		repo.Backend = domain.BackupBackendKind(tagValueNostr(event.Tags, "backend"))
	}
	if repo.RepositoryURI == "" {
		repo.RepositoryURI = firstNonEmpty(tagValueNostr(event.Tags, "repository_uri"), tagValueNostr(event.Tags, "uri"))
	}
	return &repo, nil
}

func parseBackupPolicyApplyRequest(event *nostr.Event) (*domain.BackupPolicy, error) {
	var policy domain.BackupPolicy
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &policy); err != nil {
			return nil, err
		}
	}
	if policy.Name == "" {
		policy.Name = firstNonEmpty(tagValueNostr(event.Tags, "policy"), tagValueNostr(event.Tags, "name"))
	}
	if policy.VerificationMode == "" {
		policy.VerificationMode = domain.BackupVerificationMode(tagValueNostr(event.Tags, "verification"))
	}
	return &policy, nil
}

func parseBackupRecipeApplyRequest(event *nostr.Event) (*domain.BackupRecipe, error) {
	var recipe domain.BackupRecipe
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &recipe); err != nil {
			return nil, err
		}
	}
	if recipe.Name == "" {
		recipe.Name = firstNonEmpty(tagValueNostr(event.Tags, "recipe_name"), tagValueNostr(event.Tags, "name"))
	}
	if recipe.Version == "" {
		recipe.Version = firstNonEmpty(tagValueNostr(event.Tags, "recipe_version"), tagValueNostr(event.Tags, "version"))
	}
	if recipe.Backend == "" {
		recipe.Backend = domain.BackupBackendKind(tagValueNostr(event.Tags, "backend"))
	}
	if recipe.TargetRef == "" {
		recipe.TargetRef = tagValueNostr(event.Tags, "target")
	}
	if recipe.RepositoryID == uuid.Nil {
		if id, err := parseOptionalUUID(tagValueNostr(event.Tags, "repository_id")); err != nil {
			return nil, fmt.Errorf("repository_id must be a UUID")
		} else {
			recipe.RepositoryID = id
		}
	}
	if recipe.PolicyID == nil {
		if id, err := parseOptionalUUID(tagValueNostr(event.Tags, "policy_id")); err != nil {
			return nil, fmt.Errorf("policy_id must be a UUID")
		} else if id != uuid.Nil {
			recipe.PolicyID = &id
		}
	}
	return &recipe, nil
}

func parseBackupDefinitionApplyRequest(event *nostr.Event) (*domain.BackupDefinition, error) {
	var definition domain.BackupDefinition
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &definition); err != nil {
			return nil, err
		}
	}
	if definition.Name == "" {
		definition.Name = firstNonEmpty(tagValueNostr(event.Tags, "definition"), tagValueNostr(event.Tags, "name"))
	}
	if definition.RepositoryID == uuid.Nil {
		id, err := parseOptionalUUID(tagValueNostr(event.Tags, "repository_id"))
		if err != nil {
			return nil, fmt.Errorf("repository_id must be a UUID")
		}
		definition.RepositoryID = id
	}
	if definition.PolicyID == uuid.Nil {
		id, err := parseOptionalUUID(tagValueNostr(event.Tags, "policy_id"))
		if err != nil {
			return nil, fmt.Errorf("policy_id must be a UUID")
		}
		definition.PolicyID = id
	}
	if definition.RecipeID == uuid.Nil {
		id, err := parseOptionalUUID(tagValueNostr(event.Tags, "recipe_id"))
		if err != nil {
			return nil, fmt.Errorf("recipe_id must be a UUID")
		}
		definition.RecipeID = id
	}
	return &definition, nil
}

func parseBackupRepositoryProbeRequest(event *nostr.Event) (*backupRepositoryProbeRequest, error) {
	var req backupRepositoryProbeRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.RepositoryID == "" {
		req.RepositoryID = tagValueNostr(event.Tags, "repository_id")
	}
	if req.Repository == "" {
		req.Repository = tagValueNostr(event.Tags, "repository")
	}
	if strings.TrimSpace(req.RepositoryID) == "" && strings.TrimSpace(req.Repository) == "" {
		return nil, fmt.Errorf("repository_id or repository is required")
	}
	return &req, nil
}

func parseBackupVerificationRequest(event *nostr.Event) (*backupVerificationRequest, error) {
	var req backupVerificationRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.BackupRunID == "" {
		req.BackupRunID = firstNonEmpty(tagValueNostr(event.Tags, "backup_run_id"), tagValueNostr(event.Tags, "run"))
	}
	if req.Mode == "" {
		req.Mode = domain.BackupVerificationMode(tagValueNostr(event.Tags, "verification_mode"))
	}
	if strings.TrimSpace(req.BackupRunID) == "" {
		return nil, fmt.Errorf("backup_run_id is required")
	}
	req.BackupRunID = strings.TrimSpace(req.BackupRunID)
	return &req, nil
}

func (r *Reactor) resolveBackupRepository(ctx context.Context, repositoryID, name string) (*domain.BackupRepository, error) {
	if strings.TrimSpace(repositoryID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(repositoryID))
		if err != nil {
			return nil, fmt.Errorf("invalid repository_id: %w", err)
		}
		repo, err := r.backupRegistry.GetRepository(ctx, id)
		if err != nil {
			return nil, err
		}
		if repo == nil {
			return nil, fmt.Errorf("backup repository %s not found", id)
		}
		return repo, nil
	}
	registry, ok := r.backupRegistry.(backupRegistryMutationRegistry)
	if !ok {
		return nil, fmt.Errorf("backup repository lookup by name is not configured")
	}
	repo, err := registry.GetRepositoryByName(ctx, strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, fmt.Errorf("backup repository %q not found", strings.TrimSpace(name))
	}
	return repo, nil
}

func (r *Reactor) publishBackupRegistryMutationResult(ctx context.Context, requestEvent *nostr.Event, resultKind int, action, status, message string, payload map[string]any, extraTags nostr.Tags) error {
	content := map[string]any{"request_event_id": requestEvent.ID, "action": action, "status": status, "message": message, "created_at": time.Now().UTC().Format(time.RFC3339)}
	for k, v := range payload {
		if v != nil {
			content[k] = v
		}
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{{"d", "result:" + requestEvent.ID}, {"e", requestEvent.ID, "", "reply"}, {"p", requestEvent.PubKey}, {"status", status}, {"result", action}}
	tags = append(tags, extraTags...)
	tags = appendBackupRequestTags(tags, requestEvent)
	event := &nostr.Event{Kind: resultKind, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign backup registry result: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func parseOptionalUUID(raw string) (uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(raw)
}

func mergeMetadata(dst map[string]any, src map[string]any) {
	for k, v := range src {
		if v != nil {
			dst[k] = v
		}
	}
}
