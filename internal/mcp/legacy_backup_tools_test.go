package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (s *Server) handleListBackupRepositories(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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
	repo, errResult := s.legacyRequireBackupReadModels()
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

// legacyBackupReadModelRepository exposes durable backup control-plane read models to MCP query tools.
type legacyBackupReadModelRepository interface {
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

func (s *Server) legacyRequireBackupReadModels() (legacyBackupReadModelRepository, *ToolResult) {
	if legacyFor(s).BackupReadModels == nil {
		return nil, errorResult("backup read-model repository is not configured")
	}
	return legacyFor(s).BackupReadModels, nil
}
