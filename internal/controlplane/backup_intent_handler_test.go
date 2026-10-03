package controlplane

import (
	"context"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- BackupIntentHandler tests -----------------------------------------------
//
// Phase 3 B1 tests: intent handler operations, permission, fleet scope, and
// idempotent intent_id.

func TestBackupIntentHandler_IsFleetScoped(t *testing.T) {
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{Logger: zap.NewNop()})
	if !h.IsFleetScoped() {
		t.Error("backup intent handler must be fleet-scoped")
	}
}

func TestBackupIntentHandler_PermissionForManageBackups(t *testing.T) {
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{Logger: zap.NewNop()})
	perm := h.PermissionFor("recipe-apply")
	if perm != domain.PermManageBackups {
		t.Errorf("expected PermManageBackups, got %v", perm)
	}
	// Same permission for all ops.
	perm2 := h.PermissionFor("run")
	if perm2 != domain.PermManageBackups {
		t.Errorf("expected PermManageBackups for 'run', got %v", perm2)
	}
}

func TestBackupIntentHandler_UnknownOpReturnsError(t *testing.T) {
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{Logger: zap.NewNop()})
	intent := &Intent{
		Op:      "unknown-op",
		Content: map[string]any{},
		Event:   &gonostr.Event{},
	}
	err := h.HandleIntent(context.Background(), intent)
	if err == nil {
		t.Error("expected error for unknown op")
	}
}

func TestBackupIntentHandler_RecipeApply(t *testing.T) {
	registry := newFakeBackupIntentRegistry()
	publisher := &fakeBackupIntentPublisher{}
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{
		Registry:  registry,
		Publisher: publisher,
		Logger:    zap.NewNop(),
	})

	repoID := uuid.New()
	registry.repositories[repoID] = &domain.BackupRepository{ID: repoID, Name: "test-repo", Backend: domain.BackupBackendKopia}

	intent := &Intent{
		Op: "recipe-apply",
		Content: map[string]any{
			"name":          "daily-backup",
			"version":       "v1.0",
			"backend":       "kopia",
			"repository_id": repoID.String(),
			"target_ref":    "/data",
		},
		Event: &gonostr.Event{},
	}

	if err := h.HandleIntent(context.Background(), intent); err != nil {
		t.Fatalf("HandleIntent: %v", err)
	}

	if len(registry.recipes) != 1 {
		t.Errorf("expected 1 recipe, got %d", len(registry.recipes))
	}
	if publisher.recipeCount != 1 {
		t.Errorf("expected 1 recipe publish, got %d", publisher.recipeCount)
	}
}

func TestBackupIntentHandler_RunIdempotent(t *testing.T) {
	registry := newFakeBackupIntentRegistry()
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{
		Registry: registry,
		Logger:   zap.NewNop(),
	})

	runID := uuid.New()
	// Pre-create the run to simulate idempotent re-delivery.
	registry.runs[runID] = &domain.BackupRun{ID: runID, RecipeID: uuid.New(), RepositoryID: uuid.New(), Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia}
	registry.runCreatedAlready[runID] = true

	intent := &Intent{
		Op: "run",
		Content: map[string]any{
			"id":            runID.String(),
			"recipe_id":     uuid.New().String(),
			"repository_id": uuid.New().String(),
			"backend":       "kopia",
		},
		Actor: "npub1test",
		Event: &gonostr.Event{},
	}

	// Should succeed without error (idempotent).
	if err := h.HandleIntent(context.Background(), intent); err != nil {
		t.Fatalf("HandleIntent (idempotent): %v", err)
	}
}

func TestBackupIntentHandler_Delete(t *testing.T) {
	registry := newFakeBackupIntentRegistry()
	publisher := &fakeBackupIntentPublisher{}
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{
		Registry:  registry,
		Publisher: publisher,
		Logger:    zap.NewNop(),
	})

	recipeID := uuid.New()
	registry.recipes[recipeID] = &domain.BackupRecipe{ID: recipeID, Name: "to-delete", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: uuid.New(), TargetRef: "/"}

	intent := &Intent{
		Op: "delete",
		Content: map[string]any{
			"entity_type": "recipe",
			"entity_id":   recipeID.String(),
		},
		Event: &gonostr.Event{},
	}

	if err := h.HandleIntent(context.Background(), intent); err != nil {
		t.Fatalf("HandleIntent delete: %v", err)
	}
	if publisher.deletedCount != 1 {
		t.Errorf("expected 1 delete publish, got %d", publisher.deletedCount)
	}
}

// --- Test helpers ---

type fakeBackupIntentRegistry struct {
	recipes           map[uuid.UUID]*domain.BackupRecipe
	policies          map[uuid.UUID]*domain.BackupPolicy
	repositories      map[uuid.UUID]*domain.BackupRepository
	runs              map[uuid.UUID]*domain.BackupRun
	runCreatedAlready map[uuid.UUID]bool
	restores          map[uuid.UUID]*domain.BackupRestoreRun
	verifications     map[uuid.UUID]*domain.BackupVerificationRecord
	retentionRuns     map[uuid.UUID]*domain.BackupRetentionRun
	restoreApprovals  int
}

func newFakeBackupIntentRegistry() *fakeBackupIntentRegistry {
	return &fakeBackupIntentRegistry{
		recipes:           make(map[uuid.UUID]*domain.BackupRecipe),
		policies:          make(map[uuid.UUID]*domain.BackupPolicy),
		repositories:      make(map[uuid.UUID]*domain.BackupRepository),
		runs:              make(map[uuid.UUID]*domain.BackupRun),
		runCreatedAlready: make(map[uuid.UUID]bool),
		restores:          make(map[uuid.UUID]*domain.BackupRestoreRun),
		verifications:     make(map[uuid.UUID]*domain.BackupVerificationRecord),
		retentionRuns:     make(map[uuid.UUID]*domain.BackupRetentionRun),
	}
}

func (r *fakeBackupIntentRegistry) CreateOrUpdateRecipe(_ context.Context, recipe *domain.BackupRecipe) error {
	r.recipes[recipe.ID] = recipe
	return nil
}

func (r *fakeBackupIntentRegistry) GetRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	if v, ok := r.recipes[id]; ok {
		return v, nil
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) CreateOrUpdatePolicy(_ context.Context, policy *domain.BackupPolicy) error {
	r.policies[policy.ID] = policy
	return nil
}

func (r *fakeBackupIntentRegistry) GetPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	if v, ok := r.policies[id]; ok {
		return v, nil
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) CreateOrUpdateRepository(_ context.Context, repo *domain.BackupRepository) error {
	r.repositories[repo.ID] = repo
	return nil
}

func (r *fakeBackupIntentRegistry) GetRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	if v, ok := r.repositories[id]; ok {
		return v, nil
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) GetRepositoryByName(_ context.Context, name string) (*domain.BackupRepository, error) {
	for _, repo := range r.repositories {
		if repo.Name == name {
			return repo, nil
		}
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) GetPolicyByName(_ context.Context, name string) (*domain.BackupPolicy, error) {
	for _, p := range r.policies {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) CreateBackupRunIfAbsent(_ context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	if r.runCreatedAlready[run.ID] {
		return r.runs[run.ID], false, nil
	}
	r.runs[run.ID] = run
	return run, true, nil
}

func (r *fakeBackupIntentRegistry) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	if v, ok := r.runs[id]; ok {
		return v, nil
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) CreateBackupRestoreIfAbsent(_ context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	r.restores[restore.ID] = restore
	return restore, true, nil
}

func (r *fakeBackupIntentRegistry) GetBackupRestore(_ context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	if v, ok := r.restores[id]; ok {
		return v, nil
	}
	return nil, nil
}

func (r *fakeBackupIntentRegistry) RecordBackupVerification(_ context.Context, record *domain.BackupVerificationRecord) error {
	r.verifications[record.ID] = record
	return nil
}

func (r *fakeBackupIntentRegistry) CreateBackupRetentionRunIfAbsent(_ context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	r.retentionRuns[run.ID] = run
	return run, true, nil
}

type fakeBackupIntentPublisher struct {
	recipeCount     int
	policyCount     int
	repositoryCount int
	definitionCount int
	deletedCount    int
}

func (p *fakeBackupIntentPublisher) PublishRecipe(_ context.Context, _ *domain.BackupRecipe) error {
	p.recipeCount++
	return nil
}

func (p *fakeBackupIntentPublisher) PublishPolicy(_ context.Context, _ *domain.BackupPolicy) error {
	p.policyCount++
	return nil
}

func (p *fakeBackupIntentPublisher) PublishRepository(_ context.Context, _ *domain.BackupRepository) error {
	p.repositoryCount++
	return nil
}

func (p *fakeBackupIntentPublisher) PublishDefinition(_ context.Context, _ *domain.BackupDefinition) error {
	p.definitionCount++
	return nil
}

func (p *fakeBackupIntentPublisher) PublishDeleted(_ context.Context, _ int, _ string, _ gonostr.Tags, _ string, _ string, _ *uuid.UUID) error {
	p.deletedCount++
	return nil
}

func (r *fakeBackupIntentRegistry) ApplyBackupRestoreApproval(_ context.Context, id uuid.UUID, approved bool, _, _, _ string, _ ...any) (*domain.BackupRestoreRun, bool, error) {
	restore := r.restores[id]
	if restore == nil {
		return nil, false, fmt.Errorf("restore not found")
	}
	r.restoreApprovals++
	if approved {
		restore.ApprovalStatus = domain.BackupApprovalApproved
	} else {
		restore.ApprovalStatus = domain.BackupApprovalRejected
	}
	return restore, true, nil
}

func TestBackupIntentHandler_RestoreApproval(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	registry := newFakeBackupIntentRegistry()
	registry.restores[id] = &domain.BackupRestoreRun{ID: id, ApprovalStatus: domain.BackupApprovalPending, UpdatedAt: time.Unix(1790985600, 0).UTC()}
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, Logger: zap.NewNop()})
	statuses := &statusCollector{}
	proc := NewIntentProcessor(NewTrustSet([]string{testPubkey}, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): "0000000000000000000000000000000000000000000000000000000000000001"})), openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
	proc.RegisterHandler("backup", handler)
	intent := &Intent{Domain: "backup", Op: "restore-approval", OrgID: testOrgID(), Actor: testPubkey, IntentID: "restore-approval-1", Coordinate: id.String(), Content: map[string]any{"restore_id": id.String(), "decision": "approve"}}
	require.NoError(t, proc.ProcessInProcess(ctx, intent))
	require.Equal(t, 1, registry.restoreApprovals)
	require.Equal(t, domain.BackupApprovalApproved, registry.restores[id].ApprovalStatus)
	require.Len(t, statuses.events, 1)
	require.NoError(t, proc.ProcessInProcess(ctx, intent))
	require.Equal(t, 1, registry.restoreApprovals)
	stale := *intent
	stale.IntentID = "restore-approval-stale"
	revision := time.Unix(1790985500, 0).UTC()
	stale.ExpectedUpdatedAt = &revision
	require.Error(t, proc.ProcessInProcess(ctx, &stale))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[1].Tags, "status"))
	require.Equal(t, 1, registry.restoreApprovals)
	matching := *intent
	matching.IntentID = "restore-approval-matching"
	matching.Content = map[string]any{"restore_id": id.String(), "decision": "approve",
		"expected_updated_at": registry.restores[id].UpdatedAt.Format(time.RFC3339Nano)}
	require.NoError(t, proc.ProcessInProcess(ctx, &matching))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[2].Tags, "status"))
	require.Equal(t, 2, registry.restoreApprovals)
	denied := *intent
	denied.IntentID = "restore-approval-denied"
	denied.Actor = "0000000000000000000000000000000000000000000000000000000000000001"
	require.Error(t, proc.ProcessInProcess(ctx, &denied))
	require.Equal(t, "rejected", tagValueNostr(statuses.events[3].Tags, "status"))
	require.Equal(t, 2, registry.restoreApprovals)
}
