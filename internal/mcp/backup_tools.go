package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

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
	default:
		return errorResult(fmt.Sprintf("unknown backup tool: %s", name)), nil
	}
}

func (s *Server) handleApplyBackupRepository(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "apply_backup_repository", args)
}

func (s *Server) handleApplyBackupPolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "apply_backup_policy", args)
}

func (s *Server) handleApplyBackupRecipe(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "apply_backup_recipe", args)
}

func (s *Server) handleApplyBackupDefinition(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "apply_backup_definition", args)
}

func (s *Server) handleProbeBackupRepository(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "probe_backup_repository", args)
}

func (s *Server) handleRequestBackupRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "request_backup_run", args)
}

func (s *Server) handleRequestBackupVerification(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "request_backup_verification", args)
}

func (s *Server) handleRequestBackupRestore(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "request_backup_restore", args)
}

func (s *Server) handleBackupRestoreApproval(ctx context.Context, args map[string]interface{}, approved bool) (*ToolResult, error) {
	name := "reject_backup_restore"
	if approved {
		name = "approve_backup_restore"
	}
	return s.invokeIntentWrite(ctx, name, args)
}

func (s *Server) handleRequestBackupRetention(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "request_backup_retention", args)
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

func parseBackupRunIDArg(args map[string]interface{}) (uuid.UUID, error) {
	if value := strings.TrimSpace(stringArg(args, "backup_run_id")); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid backup_run_id: %w", err)
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
