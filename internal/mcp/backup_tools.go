package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BackupCommandPublisher emits canonical backup control-plane request events.
type BackupCommandPublisher interface {
	PublishBackupRepositoryRegisterRequest(ctx context.Context, cmd BackupRepositoryApplyCommand) (*BackupCommandReceipt, error)
	PublishBackupPolicyApplyRequest(ctx context.Context, cmd BackupPolicyApplyCommand) (*BackupCommandReceipt, error)
	PublishBackupRecipeApplyRequest(ctx context.Context, cmd BackupRecipeApplyCommand) (*BackupCommandReceipt, error)
	PublishBackupDefinitionApplyRequest(ctx context.Context, cmd BackupDefinitionApplyCommand) (*BackupCommandReceipt, error)
	PublishBackupRepositoryProbeRequest(ctx context.Context, cmd BackupRepositoryProbeCommand) (*BackupCommandReceipt, error)
	PublishBackupRunRequest(ctx context.Context, cmd BackupRunCommand) (*BackupCommandReceipt, error)
	PublishBackupVerificationRequest(ctx context.Context, cmd BackupVerificationCommand) (*BackupCommandReceipt, error)
	PublishBackupRestoreRequest(ctx context.Context, cmd BackupRestoreCommand) (*BackupCommandReceipt, error)
	PublishBackupRestoreApprovalRequest(ctx context.Context, cmd BackupRestoreApprovalCommand) (*BackupCommandReceipt, error)
	PublishBackupRetentionRequest(ctx context.Context, cmd BackupRetentionCommand) (*BackupCommandReceipt, error)
}

// BackupReadModelRepository exposes durable backup control-plane read models to MCP query tools.
type BackupReadModelRepository interface {
	GetBackupRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error)
	GetBackupRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error)
	ListBackupRepositories(ctx context.Context, limit, offset int) ([]domain.BackupRepository, error)
	GetBackupPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error)
	GetBackupPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error)
	ListBackupPolicies(ctx context.Context, limit, offset int) ([]domain.BackupPolicy, error)
	GetBackupRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error)
	GetBackupRecipeByNameVersion(ctx context.Context, name, version string) (*domain.BackupRecipe, error)
	ListBackupRecipes(ctx context.Context, limit, offset int) ([]domain.BackupRecipe, error)
	GetBackupDefinition(ctx context.Context, id uuid.UUID) (*domain.BackupDefinition, error)
	GetBackupDefinitionByName(ctx context.Context, name string) (*domain.BackupDefinition, error)
	ListBackupDefinitions(ctx context.Context, limit, offset int) ([]domain.BackupDefinition, error)
	GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error)
	ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error)
	GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error)
	ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error)
	GetBackupRetentionRun(ctx context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error)
	ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error)
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

type BackupCommandOptions struct {
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type BackupRepositoryApplyCommand struct {
	Repository     domain.BackupRepository `json:"repository"`
	IdempotencyKey string                  `json:"idempotency_key,omitempty"`
	AgentID        string                  `json:"agent_id,omitempty"`
}

type BackupPolicyApplyCommand struct {
	Policy         domain.BackupPolicy `json:"policy"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
	AgentID        string              `json:"agent_id,omitempty"`
}

type BackupRecipeApplyCommand struct {
	Recipe         domain.BackupRecipe `json:"recipe"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
	AgentID        string              `json:"agent_id,omitempty"`
}

type BackupDefinitionApplyCommand struct {
	Definition     domain.BackupDefinition `json:"definition"`
	IdempotencyKey string                  `json:"idempotency_key,omitempty"`
	AgentID        string                  `json:"agent_id,omitempty"`
}

type BackupRepositoryProbeCommand struct {
	RepositoryID   uuid.UUID      `json:"repository_id,omitempty"`
	Repository     string         `json:"repository,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
}

type BackupRunCommand struct {
	RecipeID       uuid.UUID      `json:"recipe_id,omitempty"`
	Recipe         string         `json:"recipe,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
}

type BackupVerificationCommand struct {
	BackupRunID    uuid.UUID                     `json:"backup_run_id"`
	Mode           domain.BackupVerificationMode `json:"mode,omitempty"`
	Metadata       map[string]any                `json:"metadata,omitempty"`
	IdempotencyKey string                        `json:"idempotency_key,omitempty"`
	AgentID        string                        `json:"agent_id,omitempty"`
}

type BackupRestoreCommand struct {
	BackupRunID      uuid.UUID      `json:"backup_run_id"`
	RestoreTargetRef string         `json:"restore_target_ref"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
}

type BackupRestoreApprovalCommand struct {
	RestoreID      uuid.UUID      `json:"restore_id"`
	Approved       bool           `json:"approved"`
	Message        string         `json:"message,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	Reason         map[string]any `json:"reason,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
}

type BackupRetentionCommand struct {
	RepositoryID   uuid.UUID      `json:"repository_id"`
	PolicyID       uuid.UUID      `json:"policy_id"`
	DryRun         bool           `json:"dry_run,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
}

type BackupCommandReceipt struct {
	RequestEventID      string   `json:"request_event_id"`
	RequestPubkey       string   `json:"request_pubkey"`
	RequestKind         int      `json:"request_kind"`
	StatusKind          int      `json:"status_kind,omitempty"`
	ResultKind          int      `json:"result_kind"`
	ReadModelKinds      []int    `json:"read_model_kinds,omitempty"`
	DTag                string   `json:"d_tag,omitempty"`
	PublishedRelays     int      `json:"published_relays"`
	Action              string   `json:"action,omitempty"`
	RepositoryID        string   `json:"repository_id,omitempty"`
	RepositoryName      string   `json:"repository_name,omitempty"`
	PolicyID            string   `json:"policy_id,omitempty"`
	PolicyName          string   `json:"policy_name,omitempty"`
	RecipeID            string   `json:"recipe_id,omitempty"`
	RecipeName          string   `json:"recipe_name,omitempty"`
	RecipeVersion       string   `json:"recipe_version,omitempty"`
	DefinitionID        string   `json:"definition_id,omitempty"`
	DefinitionName      string   `json:"definition_name,omitempty"`
	BackupRunID         string   `json:"backup_run_id,omitempty"`
	VerificationID      string   `json:"verification_id,omitempty"`
	RestoreID           string   `json:"restore_id,omitempty"`
	RetentionRunID      string   `json:"retention_run_id,omitempty"`
	Decision            string   `json:"decision,omitempty"`
	PublishedRelayNames []string `json:"published_relay_names,omitempty"`
}

var backupBaseToolNames = []string{
	"apply_backup_repository",
	"apply_backup_policy",
	"apply_backup_recipe",
	"apply_backup_definition",
	"probe_backup_repository",
	"request_backup_run",
	"request_backup_verification",
	"request_backup_restore",
	"approve_backup_restore",
	"reject_backup_restore",
	"request_backup_retention",
	"list_backup_repositories",
	"list_backup_policies",
	"list_backup_recipes",
	"list_backup_definitions",
	"list_backup_runs",
	"list_backup_restores",
	"list_backup_retention_runs",
	"inspect_backup_repository",
	"inspect_backup_policy",
	"inspect_backup_recipe",
	"inspect_backup_run",
	"inspect_backup_restore",
	"inspect_backup_retention_run",
	"inspect_backup_definition",
}

var backupToolNameSet = func() map[string]string {
	out := map[string]string{}
	for _, name := range backupBaseToolNames {
		out[name] = name
		out["bahia_"+name] = name
	}
	return out
}()

func backupToolDefinitions() []Tool {
	tools := make([]Tool, 0, len(backupBaseToolNames)*2)
	for _, name := range backupBaseToolNames {
		tool := backupToolDefinition(name)
		tools = append(tools, tool)
		alias := tool
		alias.Name = "bahia_" + tool.Name
		alias.Description = tool.Description + " (Bahia-prefixed alias)"
		tools = append(tools, alias)
	}
	return tools
}

func backupToolDefinition(name string) Tool {
	return Tool{Name: name, Description: backupToolDescription(name), InputSchema: backupToolSchema(name)}
}

func backupToolDescription(name string) string {
	switch name {
	case "apply_backup_repository":
		return "Publish a Nostr-native backup repository register request and return correlation metadata"
	case "apply_backup_policy":
		return "Publish a Nostr-native backup policy apply request and return correlation metadata"
	case "apply_backup_recipe":
		return "Publish a Nostr-native backup recipe apply request and return correlation metadata"
	case "apply_backup_definition":
		return "Publish a Nostr-native backup definition apply request and return correlation metadata"
	case "probe_backup_repository":
		return "Publish a Nostr-native backup repository probe request and return correlation metadata"
	case "request_backup_run":
		return "Publish a Nostr-native backup run request and return correlation metadata"
	case "request_backup_verification":
		return "Publish a Nostr-native backup verification request and return correlation metadata"
	case "request_backup_restore":
		return "Publish a Nostr-native backup restore request and return correlation metadata"
	case "approve_backup_restore":
		return "Publish a Nostr-native backup restore approval request and return correlation metadata"
	case "reject_backup_restore":
		return "Publish a Nostr-native backup restore rejection request and return correlation metadata"
	case "request_backup_retention":
		return "Publish a Nostr-native backup retention enforcement request and return correlation metadata"
	case "inspect_backup_repository":
		return "Inspect one backup repository read model by repository_id or name"
	case "inspect_backup_policy":
		return "Inspect one backup policy read model by policy_id or name"
	case "inspect_backup_recipe":
		return "Inspect one backup recipe read model by recipe_id or name/version"
	case "inspect_backup_run":
		return "Inspect one backup run read model and its verification evidence"
	case "inspect_backup_restore":
		return "Inspect one backup restore read model by restore_id"
	case "inspect_backup_retention_run":
		return "Inspect one backup retention run read model by retention_run_id"
	case "inspect_backup_definition":
		return "Inspect one backup definition read model by definition_id or name"
	default:
		return "Read backup control-plane read models"
	}
}

func backupToolSchema(name string) map[string]interface{} {
	stringProp := map[string]interface{}{"type": "string"}
	objectProp := map[string]interface{}{"type": "object"}
	boolProp := map[string]interface{}{"type": "boolean"}
	arrayString := map[string]interface{}{"type": "array", "items": stringProp}
	props := map[string]interface{}{
		"idempotency_key": stringProp,
		"agent_id":        stringProp,
		"metadata":        objectProp,
	}
	addLimitStatus := func() {
		props["limit"] = map[string]interface{}{"type": "integer", "default": 100}
		props["offset"] = map[string]interface{}{"type": "integer", "default": 0}
		props["status"] = stringProp
	}
	required := []string{}
	switch name {
	case "apply_backup_repository":
		props["repository_id"] = stringProp
		props["id"] = stringProp
		props["name"] = stringProp
		props["backend"] = map[string]interface{}{"type": "string", "enum": []string{string(domain.BackupBackendKopia), string(domain.BackupBackendVelero)}}
		props["repository_uri"] = stringProp
		props["credential_profile"] = stringProp
		required = []string{"name", "backend", "repository_uri"}
	case "apply_backup_policy":
		props["policy_id"] = stringProp
		props["id"] = stringProp
		props["name"] = stringProp
		props["require_verification"] = boolProp
		props["verification_mode"] = map[string]interface{}{"type": "string", "enum": []string{string(domain.BackupVerificationNone), string(domain.BackupVerificationKopiaSnapshotVerify)}}
		required = []string{"name"}
	case "apply_backup_recipe":
		props["recipe_id"] = stringProp
		props["id"] = stringProp
		props["name"] = stringProp
		props["version"] = stringProp
		props["backend"] = map[string]interface{}{"type": "string", "enum": []string{string(domain.BackupBackendKopia)}}
		props["repository_id"] = stringProp
		props["policy_id"] = stringProp
		props["target_ref"] = stringProp
		props["include"] = arrayString
		props["exclude"] = arrayString
		props["verification_mode"] = map[string]interface{}{"type": "string", "enum": []string{string(domain.BackupVerificationNone), string(domain.BackupVerificationKopiaSnapshotVerify)}}
		required = []string{"name", "version", "backend", "repository_id", "target_ref"}
	case "apply_backup_definition":
		for _, field := range []string{"definition_id", "id", "name", "repository_id", "repository_name", "policy_id", "policy_name", "recipe_id", "recipe_name", "recipe_version", "schedule_expression", "schedule_jitter_window", "tenant_id", "tenant_name", "environment_id", "environment_name", "owner_pubkey", "approval_policy", "group", "created_by"} {
			props[field] = stringProp
		}
		props["schedule_enabled"] = boolProp
		props["requires_approval"] = boolProp
		props["restore_target_rules"] = objectProp
		props["executor_labels"] = arrayString
		props["capability_requirements"] = arrayString
		props["labels"] = objectProp
		required = []string{"name", "repository_id", "repository_name", "policy_id", "policy_name", "recipe_id", "recipe_name", "recipe_version"}
	case "probe_backup_repository":
		props["repository_id"] = stringProp
		props["repository"] = stringProp
	case "request_backup_run":
		props["recipe_id"] = stringProp
		props["recipe"] = stringProp
	case "request_backup_verification":
		props["backup_run_id"] = stringProp
		props["mode"] = map[string]interface{}{"type": "string", "enum": []string{string(domain.BackupVerificationKopiaSnapshotVerify)}}
		required = []string{"backup_run_id"}
	case "request_backup_restore":
		props["backup_run_id"] = stringProp
		props["restore_target_ref"] = stringProp
		required = []string{"backup_run_id", "restore_target_ref"}
	case "approve_backup_restore", "reject_backup_restore":
		props["restore_id"] = stringProp
		props["message"] = stringProp
		props["reason_code"] = stringProp
		props["reason"] = objectProp
		required = []string{"restore_id"}
	case "request_backup_retention":
		props["repository_id"] = stringProp
		props["policy_id"] = stringProp
		props["dry_run"] = boolProp
		required = []string{"repository_id", "policy_id"}
	case "list_backup_repositories", "list_backup_policies", "list_backup_recipes", "list_backup_definitions":
		props = map[string]interface{}{"limit": map[string]interface{}{"type": "integer", "default": 100}, "offset": map[string]interface{}{"type": "integer", "default": 0}}
	case "list_backup_runs", "list_backup_restores", "list_backup_retention_runs":
		props = map[string]interface{}{}
		addLimitStatus()
	case "inspect_backup_repository":
		props = map[string]interface{}{"repository_id": stringProp, "name": stringProp, "repository": stringProp}
	case "inspect_backup_policy":
		props = map[string]interface{}{"policy_id": stringProp, "name": stringProp, "policy": stringProp}
	case "inspect_backup_recipe":
		props = map[string]interface{}{"recipe_id": stringProp, "name": stringProp, "recipe": stringProp, "version": stringProp}
	case "inspect_backup_run":
		props = map[string]interface{}{"backup_run_id": stringProp, "run_id": stringProp}
		required = []string{"backup_run_id"}
	case "inspect_backup_restore":
		props = map[string]interface{}{"restore_id": stringProp}
		required = []string{"restore_id"}
	case "inspect_backup_retention_run":
		props = map[string]interface{}{"retention_run_id": stringProp}
		required = []string{"retention_run_id"}
	case "inspect_backup_definition":
		props = map[string]interface{}{"definition_id": stringProp, "name": stringProp}
	}
	if backupToolPublishesCommand(name) {
		required = append(required, "idempotency_key")
	}
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func isBackupToolName(name string) bool {
	_, ok := backupToolNameSet[name]
	return ok
}

func backupToolPublishesCommand(name string) bool {
	switch name {
	case "apply_backup_repository", "apply_backup_policy", "apply_backup_recipe", "apply_backup_definition",
		"probe_backup_repository", "request_backup_run", "request_backup_verification", "request_backup_restore",
		"approve_backup_restore", "reject_backup_restore", "request_backup_retention":
		return true
	default:
		return false
	}
}

func backupToolBaseName(name string) string {
	if base, ok := backupToolNameSet[name]; ok {
		return base
	}
	return name
}

func (s *Server) handleBackupTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	switch backupToolBaseName(name) {
	case "apply_backup_repository":
		return s.handleApplyBackupRepository(ctx, args)
	case "apply_backup_policy":
		return s.handleApplyBackupPolicy(ctx, args)
	case "apply_backup_recipe":
		return s.handleApplyBackupRecipe(ctx, args)
	case "apply_backup_definition":
		return s.handleApplyBackupDefinition(ctx, args)
	case "probe_backup_repository":
		return s.handleProbeBackupRepository(ctx, args)
	case "request_backup_run":
		return s.handleRequestBackupRun(ctx, args)
	case "request_backup_verification":
		return s.handleRequestBackupVerification(ctx, args)
	case "request_backup_restore":
		return s.handleRequestBackupRestore(ctx, args)
	case "approve_backup_restore":
		return s.handleBackupRestoreApproval(ctx, args, true)
	case "reject_backup_restore":
		return s.handleBackupRestoreApproval(ctx, args, false)
	case "request_backup_retention":
		return s.handleRequestBackupRetention(ctx, args)
	case "list_backup_repositories":
		return s.handleListBackupRepositories(ctx, args)
	case "list_backup_policies":
		return s.handleListBackupPolicies(ctx, args)
	case "list_backup_recipes":
		return s.handleListBackupRecipes(ctx, args)
	case "list_backup_definitions":
		return s.handleListBackupDefinitions(ctx, args)
	case "list_backup_runs":
		return s.handleListBackupRuns(ctx, args)
	case "list_backup_restores":
		return s.handleListBackupRestores(ctx, args)
	case "list_backup_retention_runs":
		return s.handleListBackupRetentionRuns(ctx, args)
	case "inspect_backup_repository":
		return s.handleInspectBackupRepository(ctx, args)
	case "inspect_backup_policy":
		return s.handleInspectBackupPolicy(ctx, args)
	case "inspect_backup_recipe":
		return s.handleInspectBackupRecipe(ctx, args)
	case "inspect_backup_run":
		return s.handleInspectBackupRun(ctx, args)
	case "inspect_backup_restore":
		return s.handleInspectBackupRestore(ctx, args)
	case "inspect_backup_retention_run":
		return s.handleInspectBackupRetentionRun(ctx, args)
	case "inspect_backup_definition":
		return s.handleInspectBackupDefinition(ctx, args)
	default:
		return errorResult(fmt.Sprintf("unknown backup tool: %s", name)), nil
	}
}

func (s *Server) requireBackupCommands() (BackupCommandPublisher, *ToolResult) {
	if s.backupCommands == nil {
		return nil, errorResult("backup command publisher is not configured")
	}
	return s.backupCommands, nil
}

func (s *Server) requireBackupReadModels() (BackupReadModelRepository, *ToolResult) {
	if s.backupReadModels == nil {
		return nil, errorResult("backup read-model repository is not configured")
	}
	return s.backupReadModels, nil
}

func (s *Server) handleApplyBackupRepository(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	repo, err := backupRepositoryFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRepositoryRegisterRequest(ctx, BackupRepositoryApplyCommand{Repository: repo, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleApplyBackupPolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	policy, err := backupPolicyFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupPolicyApplyRequest(ctx, BackupPolicyApplyCommand{Policy: policy, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleApplyBackupRecipe(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	recipe, err := backupRecipeFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRecipeApplyRequest(ctx, BackupRecipeApplyCommand{Recipe: recipe, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleApplyBackupDefinition(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	definition, err := backupDefinitionFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupDefinitionApplyRequest(ctx, BackupDefinitionApplyCommand{Definition: definition, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleProbeBackupRepository(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	repoID, err := optionalUUIDArgStrict(args, "repository_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRepositoryProbeRequest(ctx, BackupRepositoryProbeCommand{RepositoryID: repoID, Repository: firstNonEmpty(stringArg(args, "repository"), stringArg(args, "name")), Metadata: metadata, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleRequestBackupRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	recipeID, err := optionalUUIDArgStrict(args, "recipe_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRunRequest(ctx, BackupRunCommand{RecipeID: recipeID, Recipe: stringArg(args, "recipe"), Metadata: metadata, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleRequestBackupVerification(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	runID, err := parseRequiredUUIDArg(args, "backup_run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupVerificationRequest(ctx, BackupVerificationCommand{BackupRunID: runID, Mode: domain.BackupVerificationMode(stringArg(args, "mode")), Metadata: metadata, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleRequestBackupRestore(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	runID, err := parseRequiredUUIDArg(args, "backup_run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	target := strings.TrimSpace(stringArg(args, "restore_target_ref"))
	if target == "" {
		return errorResult("restore_target_ref is required"), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRestoreRequest(ctx, BackupRestoreCommand{BackupRunID: runID, RestoreTargetRef: target, Metadata: metadata, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleBackupRestoreApproval(ctx context.Context, args map[string]interface{}, approved bool) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	restoreID, err := parseRequiredUUIDArg(args, "restore_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	reason, err := optionalMapArg(args, "reason")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRestoreApprovalRequest(ctx, BackupRestoreApprovalCommand{RestoreID: restoreID, Approved: approved, Message: stringArg(args, "message"), ReasonCode: stringArg(args, "reason_code"), Reason: reason, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleRequestBackupRetention(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireBackupCommands()
	if errResult != nil {
		return errResult, nil
	}
	idempotencyKey, agentID, errResult := backupCommandIdentity(args)
	if errResult != nil {
		return errResult, nil
	}
	repositoryID, err := parseRequiredUUIDArg(args, "repository_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	policyID, err := parseRequiredUUIDArg(args, "policy_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishBackupRetentionRequest(ctx, BackupRetentionCommand{RepositoryID: repositoryID, PolicyID: policyID, DryRun: boolArg(args, "dry_run"), Metadata: metadata, IdempotencyKey: idempotencyKey, AgentID: agentID})
	return backupReceiptResult("submitted", receipt, err)
}

func (s *Server) handleListBackupRepositories(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupRepositories(ctx, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup repositories: %v", err)), nil
	}
	return jsonResult(map[string]any{"repositories": items, "total": len(items)})
}

func (s *Server) handleListBackupPolicies(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupPolicies(ctx, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup policies: %v", err)), nil
	}
	return jsonResult(map[string]any{"policies": items, "total": len(items)})
}

func (s *Server) handleListBackupRecipes(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupRecipes(ctx, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup recipes: %v", err)), nil
	}
	return jsonResult(map[string]any{"recipes": items, "total": len(items)})
}

func (s *Server) handleListBackupDefinitions(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupDefinitions(ctx, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup definitions: %v", err)), nil
	}
	return jsonResult(map[string]any{"definitions": items, "total": len(items)})
}

func (s *Server) handleListBackupRuns(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	status, err := backupRunStatusArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupRuns(ctx, status, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup runs: %v", err)), nil
	}
	return jsonResult(map[string]any{"runs": items, "total": len(items)})
}

func (s *Server) handleListBackupRestores(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	status, err := backupRunStatusArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupRestores(ctx, status, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup restores: %v", err)), nil
	}
	return jsonResult(map[string]any{"restores": items, "total": len(items)})
}

func (s *Server) handleListBackupRetentionRuns(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	status, err := backupRunStatusArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	items, err := repo.ListBackupRetentionRuns(ctx, status, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list backup retention runs: %v", err)), nil
	}
	return jsonResult(map[string]any{"retention_runs": items, "total": len(items)})
}

func (s *Server) handleInspectBackupRepository(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	id, err := optionalUUIDArgStrict(args, "repository_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	var item *domain.BackupRepository
	if id != uuid.Nil {
		item, err = repo.GetBackupRepository(ctx, id)
	} else {
		name := firstNonEmpty(stringArg(args, "name"), stringArg(args, "repository"))
		if strings.TrimSpace(name) == "" {
			return errorResult("repository_id, name, or repository is required"), nil
		}
		item, err = repo.GetBackupRepositoryByName(ctx, name)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup repository: %v", err)), nil
	}
	if item == nil {
		return errorResult("backup repository not found"), nil
	}
	return jsonResult(map[string]any{"repository": item})
}

func (s *Server) handleInspectBackupPolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	id, err := optionalUUIDArgStrict(args, "policy_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	var item *domain.BackupPolicy
	if id != uuid.Nil {
		item, err = repo.GetBackupPolicy(ctx, id)
	} else {
		name := firstNonEmpty(stringArg(args, "name"), stringArg(args, "policy"))
		if strings.TrimSpace(name) == "" {
			return errorResult("policy_id, name, or policy is required"), nil
		}
		item, err = repo.GetBackupPolicyByName(ctx, name)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup policy: %v", err)), nil
	}
	if item == nil {
		return errorResult("backup policy not found"), nil
	}
	return jsonResult(map[string]any{"policy": item})
}

func (s *Server) handleInspectBackupRecipe(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	id, err := optionalUUIDArgStrict(args, "recipe_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	var item *domain.BackupRecipe
	if id != uuid.Nil {
		item, err = repo.GetBackupRecipe(ctx, id)
	} else {
		name := firstNonEmpty(stringArg(args, "name"), stringArg(args, "recipe"))
		version := stringArg(args, "version")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			return errorResult("recipe_id or name/version is required"), nil
		}
		item, err = repo.GetBackupRecipeByNameVersion(ctx, name, version)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup recipe: %v", err)), nil
	}
	if item == nil {
		return errorResult("backup recipe not found"), nil
	}
	return jsonResult(map[string]any{"recipe": item})
}

func (s *Server) handleInspectBackupRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	runID, err := parseBackupRunIDArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	run, err := repo.GetBackupRun(ctx, runID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup run: %v", err)), nil
	}
	if run == nil {
		return errorResult("backup run not found"), nil
	}
	verification, verificationErr := repo.GetBackupVerificationByRunID(ctx, runID)
	result := map[string]any{"run": run}
	if verificationErr == nil && verification != nil {
		result["verification"] = verification
	} else if verificationErr != nil {
		result["verification_error"] = verificationErr.Error()
	}
	return jsonResult(result)
}

func (s *Server) handleInspectBackupRestore(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	restoreID, err := parseRequiredUUIDArg(args, "restore_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	restore, err := repo.GetBackupRestore(ctx, restoreID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup restore: %v", err)), nil
	}
	if restore == nil {
		return errorResult("backup restore not found"), nil
	}
	return jsonResult(map[string]any{"restore": restore})
}

func (s *Server) handleInspectBackupRetentionRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	retentionRunID, err := parseRequiredUUIDArg(args, "retention_run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	run, err := repo.GetBackupRetentionRun(ctx, retentionRunID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup retention run: %v", err)), nil
	}
	if run == nil {
		return errorResult("backup retention run not found"), nil
	}
	return jsonResult(map[string]any{"retention_run": run})
}

func (s *Server) handleInspectBackupDefinition(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.requireBackupReadModels()
	if errResult != nil {
		return errResult, nil
	}
	id, err := optionalUUIDArgStrict(args, "definition_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	var item *domain.BackupDefinition
	if id != uuid.Nil {
		item, err = repo.GetBackupDefinition(ctx, id)
	} else {
		name := stringArg(args, "name")
		if strings.TrimSpace(name) == "" {
			return errorResult("definition_id or name is required"), nil
		}
		item, err = repo.GetBackupDefinitionByName(ctx, name)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("failed to inspect backup definition: %v", err)), nil
	}
	if item == nil {
		return errorResult("backup definition not found"), nil
	}
	return jsonResult(map[string]any{"definition": item})
}

func backupReceiptResult(status string, receipt *BackupCommandReceipt, err error) (*ToolResult, error) {
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(backupReceiptToMap(status, receipt))
}

func backupCommandIdentity(args map[string]interface{}) (string, string, *ToolResult) {
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotency_key"))
	if idempotencyKey == "" {
		return "", "", errorResult("idempotency_key is required for addressable backup command events")
	}
	return idempotencyKey, strings.TrimSpace(stringArg(args, "agent_id")), nil
}

func backupReceiptToMap(status string, receipt *BackupCommandReceipt) map[string]any {
	result := map[string]any{"status": status}
	if receipt == nil {
		return result
	}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	if receipt.StatusKind > 0 {
		result["status_kind"] = receipt.StatusKind
		result["status_kinds"] = []int{receipt.StatusKind}
	}
	result["result_kind"] = receipt.ResultKind
	result["result_kinds"] = []int{receipt.ResultKind}
	if len(receipt.ReadModelKinds) > 0 {
		result["read_model_kinds"] = receipt.ReadModelKinds
	}
	result["d_tag"] = receipt.DTag
	result["published_relays"] = receipt.PublishedRelays
	if len(receipt.PublishedRelayNames) > 0 {
		result["published_relay_names"] = receipt.PublishedRelayNames
	}
	for key, value := range map[string]string{
		"action": receipt.Action, "repository_id": receipt.RepositoryID, "repository_name": receipt.RepositoryName,
		"policy_id": receipt.PolicyID, "policy_name": receipt.PolicyName, "recipe_id": receipt.RecipeID,
		"recipe_name": receipt.RecipeName, "recipe_version": receipt.RecipeVersion, "definition_id": receipt.DefinitionID,
		"definition_name": receipt.DefinitionName, "backup_run_id": receipt.BackupRunID, "verification_id": receipt.VerificationID,
		"restore_id": receipt.RestoreID, "retention_run_id": receipt.RetentionRunID, "decision": receipt.Decision,
	} {
		if strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func backupRepositoryFromArgs(args map[string]interface{}) (domain.BackupRepository, error) {
	var repo domain.BackupRepository
	if err := decodeBackupArgs(args, &repo); err != nil {
		return repo, err
	}
	if repo.ID == uuid.Nil {
		repo.ID = firstUUIDArg(args, "repository_id", "id")
	}
	if err := domain.ValidateBackupRepository(&repo); err != nil {
		return repo, err
	}
	return repo, nil
}

func backupPolicyFromArgs(args map[string]interface{}) (domain.BackupPolicy, error) {
	var policy domain.BackupPolicy
	if err := decodeBackupArgs(args, &policy); err != nil {
		return policy, err
	}
	if policy.ID == uuid.Nil {
		policy.ID = firstUUIDArg(args, "policy_id", "id")
	}
	if err := domain.ValidateBackupPolicy(&policy); err != nil {
		return policy, err
	}
	return policy, nil
}

func backupRecipeFromArgs(args map[string]interface{}) (domain.BackupRecipe, error) {
	var recipe domain.BackupRecipe
	if err := decodeBackupArgs(args, &recipe); err != nil {
		return recipe, err
	}
	if recipe.ID == uuid.Nil {
		recipe.ID = firstUUIDArg(args, "recipe_id", "id")
	}
	if err := domain.ValidateBackupRecipe(&recipe); err != nil {
		return recipe, err
	}
	return recipe, nil
}

func backupDefinitionFromArgs(args map[string]interface{}) (domain.BackupDefinition, error) {
	var definition domain.BackupDefinition
	if err := decodeBackupArgs(args, &definition); err != nil {
		return definition, err
	}
	if definition.ID == uuid.Nil {
		definition.ID = firstUUIDArg(args, "definition_id", "id")
	}
	if err := domain.ValidateBackupDefinition(&definition); err != nil {
		return definition, err
	}
	return definition, nil
}

func decodeBackupArgs(args map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid backup payload: %w", err)
	}
	return nil
}

func firstUUIDArg(args map[string]interface{}, names ...string) uuid.UUID {
	for _, name := range names {
		if id, err := optionalUUIDArgStrict(args, name); err == nil && id != uuid.Nil {
			return id
		}
	}
	return uuid.Nil
}

func optionalUUIDArgStrict(args map[string]interface{}, name string) (uuid.UUID, error) {
	value := strings.TrimSpace(stringArg(args, name))
	if value == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %v", name, err)
	}
	return id, nil
}

func parseBackupRunIDArg(args map[string]interface{}) (uuid.UUID, error) {
	if value := strings.TrimSpace(stringArg(args, "backup_run_id")); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid backup_run_id: %v", err)
		}
		return id, nil
	}
	return parseRequiredUUIDArg(args, "run_id")
}

func backupRunStatusArg(args map[string]interface{}) (domain.DeploymentRunStatus, error) {
	status := domain.DeploymentRunStatus(strings.TrimSpace(stringArg(args, "status")))
	if status == "" {
		return "", nil
	}
	if err := domain.ValidateDeploymentRunStatus(status); err != nil {
		return "", err
	}
	return status, nil
}

func backupReadModelKinds() []int {
	return []int{controlplane.KindBackupRunStatus, controlplane.KindBackupRestoreStatus, controlplane.KindBackupVerificationStatus, controlplane.KindBackupObservation}
}
