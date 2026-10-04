package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestGetToolsIncludesBackupTools(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{}
	for _, name := range backupBaseToolNames {
		required[name] = false
		required["bahia_"+name] = false
	}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing backup tool %s", name)
		}
	}
}

type memoryBackupReadModels struct {
	repositories []domain.BackupRepository
	policies     []domain.BackupPolicy
	recipes      []domain.BackupRecipe
	definitions  []domain.BackupDefinition
	runs         []domain.BackupRun
	restores     []domain.BackupRestoreRun
	retentions   []domain.BackupRetentionRun
	verification *domain.BackupVerificationRecord
	lastStatus   domain.DeploymentRunStatus
}

func (m *memoryBackupReadModels) GetBackupRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	for i := range m.repositories {
		if m.repositories[i].ID == id {
			return &m.repositories[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) GetBackupRepositoryByName(_ context.Context, name string) (*domain.BackupRepository, error) {
	for i := range m.repositories {
		if m.repositories[i].Name == name {
			return &m.repositories[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupRepositories(context.Context, int, int) ([]domain.BackupRepository, error) {
	return m.repositories, nil
}
func (m *memoryBackupReadModels) GetBackupPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	for i := range m.policies {
		if m.policies[i].ID == id {
			return &m.policies[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) GetBackupPolicyByName(_ context.Context, name string) (*domain.BackupPolicy, error) {
	for i := range m.policies {
		if m.policies[i].Name == name {
			return &m.policies[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupPolicies(context.Context, int, int) ([]domain.BackupPolicy, error) {
	return m.policies, nil
}
func (m *memoryBackupReadModels) GetBackupRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	for i := range m.recipes {
		if m.recipes[i].ID == id {
			return &m.recipes[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) GetBackupRecipeByNameVersion(_ context.Context, name, version string) (*domain.BackupRecipe, error) {
	for i := range m.recipes {
		if m.recipes[i].Name == name && m.recipes[i].Version == version {
			return &m.recipes[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupRecipes(context.Context, int, int) ([]domain.BackupRecipe, error) {
	return m.recipes, nil
}
func (m *memoryBackupReadModels) GetBackupDefinition(_ context.Context, id uuid.UUID) (*domain.BackupDefinition, error) {
	for i := range m.definitions {
		if m.definitions[i].ID == id {
			return &m.definitions[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) GetBackupDefinitionByName(_ context.Context, name string) (*domain.BackupDefinition, error) {
	for i := range m.definitions {
		if m.definitions[i].Name == name {
			return &m.definitions[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupDefinitions(context.Context, int, int) ([]domain.BackupDefinition, error) {
	return m.definitions, nil
}
func (m *memoryBackupReadModels) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	for i := range m.runs {
		if m.runs[i].ID == id {
			return &m.runs[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupRuns(_ context.Context, status domain.DeploymentRunStatus, _, _ int) ([]domain.BackupRun, error) {
	m.lastStatus = status
	return m.runs, nil
}
func (m *memoryBackupReadModels) GetBackupRestore(_ context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	for i := range m.restores {
		if m.restores[i].ID == id {
			return &m.restores[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupRestores(_ context.Context, status domain.DeploymentRunStatus, _, _ int) ([]domain.BackupRestoreRun, error) {
	m.lastStatus = status
	return m.restores, nil
}
func (m *memoryBackupReadModels) GetBackupRetentionRun(_ context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error) {
	for i := range m.retentions {
		if m.retentions[i].ID == id {
			return &m.retentions[i], nil
		}
	}
	return nil, nil
}
func (m *memoryBackupReadModels) ListBackupRetentionRuns(_ context.Context, status domain.DeploymentRunStatus, _, _ int) ([]domain.BackupRetentionRun, error) {
	m.lastStatus = status
	return m.retentions, nil
}
func (m *memoryBackupReadModels) GetBackupVerificationByRunID(_ context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	if m.verification != nil && m.verification.BackupRunID == runID {
		return m.verification, nil
	}
	return nil, nil
}

func TestBackupReadModelToolsListAndInspectDurableBackupState(t *testing.T) {
	ctx := authorizedMCPContext()
	repoID := uuid.New()
	policyID := uuid.New()
	recipeID := uuid.New()
	definitionID := uuid.New()
	runID := uuid.New()
	restoreID := uuid.New()
	retentionRunID := uuid.New()
	readModels := &memoryBackupReadModels{
		repositories: []domain.BackupRepository{{ID: repoID, Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://archive"}},
		policies:     []domain.BackupPolicy{{ID: policyID, Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}},
		recipes:      []domain.BackupRecipe{{ID: recipeID, Name: "postgres", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, PolicyID: &policyID, TargetRef: "/srv/postgres"}},
		definitions:  []domain.BackupDefinition{{ID: definitionID, Name: "postgres-prod", RepositoryID: repoID, RepositoryName: "archive", PolicyID: policyID, PolicyName: "verified", RecipeID: recipeID, RecipeName: "postgres", RecipeVersion: "v1"}},
		runs:         []domain.BackupRun{{ID: runID, RecipeID: recipeID, RepositoryID: repoID, PolicyID: &policyID, RequestedBy: "operator", RequestEventID: "event", RequestKind: controlplane.KindBackupRunRequest, RequestDTag: "run:1", Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, TargetRef: "/srv/postgres", SnapshotCreated: true, SnapshotID: "snap-1", VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, VerificationStatus: domain.BackupVerificationSucceeded, RestoreEligibility: domain.RestoreEligibilityEligible}},
		restores:     []domain.BackupRestoreRun{{ID: restoreID, BackupRunID: runID, RecipeID: recipeID, RepositoryID: repoID, PolicyID: &policyID, SnapshotID: "snap-1", RestoreTargetRef: "/restore/postgres", RequestedBy: "operator", RequestEventID: "restore-event", RequestKind: controlplane.KindBackupRestoreRequest, RequestDTag: "restore:1", ApprovalStatus: domain.BackupApprovalNotRequired, ApprovalRequirement: domain.BackupApprovalRequirementNone, Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, VerificationStatus: domain.BackupVerificationSucceeded}},
		retentions:   []domain.BackupRetentionRun{{ID: retentionRunID, RepositoryID: repoID, PolicyID: &policyID, RequestedBy: "operator", RequestEventID: "retention-event", RequestKind: controlplane.KindBackupRetentionEnforce, RequestDTag: "retention:1", Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, DryRun: true}},
		verification: &domain.BackupVerificationRecord{ID: uuid.New(), BackupRunID: runID, Mode: domain.BackupVerificationKopiaSnapshotVerify, Status: domain.BackupVerificationSucceeded, Verified: true},
	}
	server := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{BackupReadModels: readModels})
	fixture := attachCanonicalMCPFixture(t, server)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range readModels.repositories {
		readModels.repositories[i].CreatedAt, readModels.repositories[i].UpdatedAt = now, now
	}
	for i := range readModels.policies {
		readModels.policies[i].CreatedAt, readModels.policies[i].UpdatedAt = now, now
	}
	for i := range readModels.recipes {
		readModels.recipes[i].CreatedAt, readModels.recipes[i].UpdatedAt = now, now
	}
	for i := range readModels.definitions {
		readModels.definitions[i].CreatedAt, readModels.definitions[i].UpdatedAt = now, now
	}
	for i := range readModels.runs {
		readModels.runs[i].CreatedAt, readModels.runs[i].UpdatedAt = now, now
	}
	for i := range readModels.restores {
		readModels.restores[i].CreatedAt, readModels.restores[i].UpdatedAt = now, now
	}
	for i := range readModels.retentions {
		readModels.retentions[i].CreatedAt, readModels.retentions[i].UpdatedAt = now, now
	}
	if readModels.verification != nil {
		readModels.verification.CreatedAt, readModels.verification.UpdatedAt = now, now
	}
	publisher := nostrpool.NewBackupCanonicalPublisher(fixture.projector, zap.NewNop())
	for i := range readModels.repositories {
		if err := publisher.PublishRepository(ctx, &readModels.repositories[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.policies {
		if err := publisher.PublishPolicy(ctx, &readModels.policies[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.recipes {
		if err := publisher.PublishRecipe(ctx, &readModels.recipes[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.definitions {
		if err := publisher.PublishDefinition(ctx, &readModels.definitions[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.runs {
		if err := publisher.PublishRun(ctx, &readModels.runs[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.restores {
		if err := publisher.PublishRestore(ctx, &readModels.restores[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range readModels.retentions {
		if err := publisher.PublishRetention(ctx, &readModels.retentions[i]); err != nil {
			t.Fatal(err)
		}
	}
	if readModels.verification != nil {
		if err := publisher.PublishVerification(ctx, readModels.verification); err != nil {
			t.Fatal(err)
		}
	}

	listRes, err := server.CallTool(ctx, "bahia_list_backup_repositories", map[string]interface{}{})
	if err != nil || listRes.IsError {
		t.Fatalf("list repositories result=%#v err=%v", listRes, err)
	}
	listPayload := decodeResultMap(t, listRes)
	if listPayload["total"].(float64) != 1 {
		t.Fatalf("unexpected repository list: %#v", listPayload)
	}

	inspectRepo, err := server.CallTool(ctx, "inspect_backup_repository", map[string]interface{}{"name": "archive"})
	if err != nil || inspectRepo.IsError {
		t.Fatalf("inspect repository result=%#v err=%v", inspectRepo, err)
	}
	if decodeResultMap(t, inspectRepo)["repository"].(map[string]interface{})["name"] != "archive" {
		t.Fatalf("unexpected repository inspect payload: %#v", decodeResultMap(t, inspectRepo))
	}

	inspectPolicy, err := server.CallTool(ctx, "inspect_backup_policy", map[string]interface{}{"policy": "verified"})
	if err != nil || inspectPolicy.IsError {
		t.Fatalf("inspect policy result=%#v err=%v", inspectPolicy, err)
	}
	if decodeResultMap(t, inspectPolicy)["policy"].(map[string]interface{})["name"] != "verified" {
		t.Fatalf("unexpected policy inspect payload: %#v", decodeResultMap(t, inspectPolicy))
	}

	inspectRecipe, err := server.CallTool(ctx, "inspect_backup_recipe", map[string]interface{}{"name": "postgres", "version": "v1"})
	if err != nil || inspectRecipe.IsError {
		t.Fatalf("inspect recipe result=%#v err=%v", inspectRecipe, err)
	}
	if decodeResultMap(t, inspectRecipe)["recipe"].(map[string]interface{})["target_ref"] != "/srv/postgres" {
		t.Fatalf("unexpected recipe inspect payload: %#v", decodeResultMap(t, inspectRecipe))
	}

	inspectRun, err := server.CallTool(ctx, "inspect_backup_run", map[string]interface{}{"run_id": runID.String()})
	if err != nil || inspectRun.IsError {
		t.Fatalf("inspect run result=%#v err=%v", inspectRun, err)
	}
	runPayload := decodeResultMap(t, inspectRun)
	if runPayload["verification"].(map[string]interface{})["verified"] != true {
		t.Fatalf("inspect run did not include verification evidence: %#v", runPayload)
	}

	restoreRes, err := server.CallTool(ctx, "inspect_backup_restore", map[string]interface{}{"restore_id": restoreID.String()})
	if err != nil || restoreRes.IsError {
		t.Fatalf("inspect restore result=%#v err=%v", restoreRes, err)
	}
	if decodeResultMap(t, restoreRes)["restore"].(map[string]interface{})["restore_target_ref"] != "/restore/postgres" {
		t.Fatalf("unexpected restore inspect payload: %#v", decodeResultMap(t, restoreRes))
	}

	retentionRes, err := server.CallTool(ctx, "inspect_backup_retention_run", map[string]interface{}{"retention_run_id": retentionRunID.String()})
	if err != nil || retentionRes.IsError {
		t.Fatalf("inspect retention result=%#v err=%v", retentionRes, err)
	}
	if decodeResultMap(t, retentionRes)["retention_run"].(map[string]interface{})["dry_run"] != true {
		t.Fatalf("unexpected retention inspect payload: %#v", decodeResultMap(t, retentionRes))
	}

	definitionRes, err := server.CallTool(ctx, "inspect_backup_definition", map[string]interface{}{"definition_id": definitionID.String()})
	if err != nil || definitionRes.IsError {
		t.Fatalf("inspect definition result=%#v err=%v", definitionRes, err)
	}
	if decodeResultMap(t, definitionRes)["definition"].(map[string]interface{})["name"] != "postgres-prod" {
		t.Fatalf("unexpected definition inspect payload: %#v", decodeResultMap(t, definitionRes))
	}

	filteredRuns, err := server.CallTool(ctx, "list_backup_runs", map[string]interface{}{"status": "succeeded"})
	if err != nil {
		t.Fatalf("list runs err: %v", err)
	}
	if int(decodeResultMap(t, filteredRuns)["total"].(float64)) != 1 {
		t.Fatalf("store status filter returned wrong runs: %#v", filteredRuns)
	}
}

func TestBackupReadModelToolsWorkWithoutRepository(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(authorizedMCPContext(), "list_backup_repositories", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || res.IsError || int(decodeResultMap(t, res)["total"].(float64)) != 0 {
		t.Fatalf("expected empty store-backed result, got %#v", res)
	}
}
