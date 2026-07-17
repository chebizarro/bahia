package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type captureBackupCommandPublisher struct {
	repository *BackupRepositoryApplyCommand
	policy     *BackupPolicyApplyCommand
	recipe     *BackupRecipeApplyCommand
	definition *BackupDefinitionApplyCommand
	probe      *BackupRepositoryProbeCommand
	run        *BackupRunCommand
	verify     *BackupVerificationCommand
	restore    *BackupRestoreCommand
	approval   *BackupRestoreApprovalCommand
	retention  *BackupRetentionCommand
}

func (p *captureBackupCommandPublisher) PublishBackupRepositoryRegisterRequest(_ context.Context, cmd BackupRepositoryApplyCommand) (*BackupCommandReceipt, error) {
	p.repository = &cmd
	return backupTestReceipt(controlplane.KindBackupRepositoryRegister, controlplane.KindBackupRepositoryRegisterResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupPolicyApplyRequest(_ context.Context, cmd BackupPolicyApplyCommand) (*BackupCommandReceipt, error) {
	p.policy = &cmd
	return backupTestReceipt(controlplane.KindBackupPolicyApply, controlplane.KindBackupPolicyApplyResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRecipeApplyRequest(_ context.Context, cmd BackupRecipeApplyCommand) (*BackupCommandReceipt, error) {
	p.recipe = &cmd
	return backupTestReceipt(controlplane.KindBackupRecipeApply, controlplane.KindBackupRecipeApplyResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupDefinitionApplyRequest(_ context.Context, cmd BackupDefinitionApplyCommand) (*BackupCommandReceipt, error) {
	p.definition = &cmd
	return backupTestReceipt(controlplane.KindBackupDefinitionApply, controlplane.KindBackupDefinitionApplyResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRepositoryProbeRequest(_ context.Context, cmd BackupRepositoryProbeCommand) (*BackupCommandReceipt, error) {
	p.probe = &cmd
	return backupTestReceipt(controlplane.KindBackupRepositoryProbe, controlplane.KindBackupRepositoryProbeResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRunRequest(_ context.Context, cmd BackupRunCommand) (*BackupCommandReceipt, error) {
	p.run = &cmd
	return backupTestReceipt(controlplane.KindBackupRunRequest, controlplane.KindBackupRunResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupVerificationRequest(_ context.Context, cmd BackupVerificationCommand) (*BackupCommandReceipt, error) {
	p.verify = &cmd
	return backupTestReceipt(controlplane.KindBackupVerificationRequest, controlplane.KindBackupVerificationResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRestoreRequest(_ context.Context, cmd BackupRestoreCommand) (*BackupCommandReceipt, error) {
	p.restore = &cmd
	return backupTestReceipt(controlplane.KindBackupRestoreRequest, controlplane.KindBackupRestoreResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRestoreApprovalRequest(_ context.Context, cmd BackupRestoreApprovalCommand) (*BackupCommandReceipt, error) {
	p.approval = &cmd
	return backupTestReceipt(controlplane.KindBackupRestoreApproval, controlplane.KindBackupRestoreApprovalResult, cmd.IdempotencyKey), nil
}
func (p *captureBackupCommandPublisher) PublishBackupRetentionRequest(_ context.Context, cmd BackupRetentionCommand) (*BackupCommandReceipt, error) {
	p.retention = &cmd
	return backupTestReceipt(controlplane.KindBackupRetentionEnforce, controlplane.KindBackupRetentionResult, cmd.IdempotencyKey), nil
}

func backupTestReceipt(requestKind, resultKind int, dTag string) *BackupCommandReceipt {
	return &BackupCommandReceipt{RequestEventID: "backup-event", RequestPubkey: "operator-pubkey", RequestKind: requestKind, StatusKind: controlplane.KindBackupRunStatus, ResultKind: resultKind, ReadModelKinds: backupReadModelKinds(), DTag: dTag, PublishedRelays: 1}
}

func TestGetToolsIncludesBackupTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
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

func TestBackupMutatingToolsPublishNostrRequestsAndReturnCorrelation(t *testing.T) {
	ctx := authorizedMCPContext()
	publisher := &captureBackupCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{BackupCommandPublisher: publisher})
	repoID := uuid.New()
	policyID := uuid.New()
	recipeID := uuid.New()
	definitionID := uuid.New()
	runID := uuid.New()
	restoreID := uuid.New()

	calls := []struct {
		name string
		args map[string]interface{}
		kind int
	}{
		{"apply_backup_repository", map[string]interface{}{"id": repoID.String(), "name": "archive", "backend": "kopia", "repository_uri": "kopia://archive", "idempotency_key": "repo:1"}, controlplane.KindBackupRepositoryRegister},
		{"apply_backup_policy", map[string]interface{}{"id": policyID.String(), "name": "verified", "require_verification": true, "verification_mode": "kopia_snapshot_verify", "idempotency_key": "policy:1"}, controlplane.KindBackupPolicyApply},
		{"apply_backup_recipe", map[string]interface{}{"id": recipeID.String(), "name": "postgres", "version": "v1", "backend": "kopia", "repository_id": repoID.String(), "policy_id": policyID.String(), "target_ref": "/srv/postgres", "idempotency_key": "recipe:1"}, controlplane.KindBackupRecipeApply},
		{"apply_backup_definition", map[string]interface{}{"id": definitionID.String(), "name": "postgres-prod", "repository_id": repoID.String(), "repository_name": "archive", "policy_id": policyID.String(), "policy_name": "verified", "recipe_id": recipeID.String(), "recipe_name": "postgres", "recipe_version": "v1", "created_by": "operator", "idempotency_key": "definition:1"}, controlplane.KindBackupDefinitionApply},
		{"probe_backup_repository", map[string]interface{}{"repository_id": repoID.String(), "idempotency_key": "probe:1"}, controlplane.KindBackupRepositoryProbe},
		{"request_backup_run", map[string]interface{}{"recipe_id": recipeID.String(), "idempotency_key": "run:1"}, controlplane.KindBackupRunRequest},
		{"request_backup_verification", map[string]interface{}{"backup_run_id": runID.String(), "mode": "kopia_snapshot_verify", "idempotency_key": "verify:1"}, controlplane.KindBackupVerificationRequest},
		{"request_backup_restore", map[string]interface{}{"backup_run_id": runID.String(), "restore_target_ref": "/restore/postgres", "idempotency_key": "restore:1"}, controlplane.KindBackupRestoreRequest},
		{"approve_backup_restore", map[string]interface{}{"restore_id": restoreID.String(), "message": "approved", "idempotency_key": "approve:1"}, controlplane.KindBackupRestoreApproval},
		{"reject_backup_restore", map[string]interface{}{"restore_id": restoreID.String(), "message": "reject", "idempotency_key": "reject:1"}, controlplane.KindBackupRestoreApproval},
		{"request_backup_retention", map[string]interface{}{"repository_id": repoID.String(), "policy_id": policyID.String(), "dry_run": true, "idempotency_key": "retention:1"}, controlplane.KindBackupRetentionEnforce},
	}
	for _, call := range calls {
		res, err := server.CallTool(ctx, call.name, call.args)
		if err != nil {
			t.Fatalf("%s err: %v", call.name, err)
		}
		if res.IsError {
			t.Fatalf("%s returned error: %s", call.name, res.Content[0].Text)
		}
		payload := decodeResultMap(t, res)
		if payload["request_event_id"] != "backup-event" || payload["request_kind"].(float64) != float64(call.kind) || payload["d_tag"] == "" {
			t.Fatalf("%s missing correlation metadata: %#v", call.name, payload)
		}
	}
	if publisher.repository == nil || publisher.repository.Repository.Name != "archive" {
		t.Fatalf("repository command not captured: %#v", publisher.repository)
	}
	if publisher.approval == nil || publisher.approval.Approved {
		t.Fatalf("last approval command should be rejection: %#v", publisher.approval)
	}
	if publisher.retention == nil || !publisher.retention.DryRun {
		t.Fatalf("retention command not captured: %#v", publisher.retention)
	}
}

func TestBackupMutatingToolsRequirePublisher(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(authorizedMCPContext(), "request_backup_run", map[string]interface{}{"recipe": "postgres:v1"})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected missing publisher error, got %#v", res)
	}
}

func TestBackupMutatingToolsRequireIdempotencyKeyWhenPublisherConfigured(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{BackupCommandPublisher: &captureBackupCommandPublisher{}})
	res, err := server.CallTool(authorizedMCPContext(), "request_backup_run", map[string]interface{}{"recipe": "postgres:v1"})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected idempotency error, got %#v", res)
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
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{BackupReadModels: readModels})

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

	inspectRun, err := server.CallTool(ctx, "inspect_backup_run", map[string]interface{}{"backup_run_id": runID.String()})
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

	_, err = server.CallTool(ctx, "list_backup_runs", map[string]interface{}{"status": "succeeded"})
	if err != nil {
		t.Fatalf("list runs err: %v", err)
	}
	if readModels.lastStatus != domain.RunStatusSucceeded {
		t.Fatalf("status filter not forwarded: %q", readModels.lastStatus)
	}
}

func TestBackupReadModelToolsRequireRepository(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(authorizedMCPContext(), "list_backup_repositories", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected missing read-model repository error, got %#v", res)
	}
}
