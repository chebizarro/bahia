package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- BackupIntentHandler tests -----------------------------------------------
//
// Backup intent handler tests: operations, permission, org scope, and
// idempotent intent_id.

func TestBackupIntentHandler_IsFleetScoped(t *testing.T) {
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{Logger: zap.NewNop()})
	if h.IsFleetScoped() {
		t.Error("backup intent handler must use org backups:manage")
	}
}

type fakeBackupOwnerReader struct{ owned map[uuid.UUID]uuid.UUID }

func (f fakeBackupOwnerReader) OwnerOf(_ context.Context, _ int, id uuid.UUID) (uuid.UUID, bool, error) {
	owner, found := f.owned[id]
	return owner, found, nil
}

type fakeBackupConfigReader struct{ registry *fakeBackupIntentRegistry }

func (f fakeBackupConfigReader) ConfigOf(_ context.Context, family int, id uuid.UUID) (json.RawMessage, bool, error) {
	var value any
	switch family {
	case kinds.BackupRecipeRegistry:
		if v := f.registry.recipes[id]; v != nil {
			value = v
		}
	case kinds.BackupPolicyRegistry:
		if v := f.registry.policies[id]; v != nil {
			value = v
		}
	case kinds.BackupRepositoryRegistry:
		if v := f.registry.repositories[id]; v != nil {
			value = v
		}
	case kinds.BackupDefinitionRegistry:
		if v := f.registry.definitions[id]; v != nil {
			value = v
		}
	}
	if value == nil {
		return nil, false, nil
	}
	raw, err := json.Marshal(value)
	return raw, true, err
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
	repoID, recipeID := uuid.New(), uuid.New()
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{
		Registry:     registry,
		ConfigReader: fakeBackupConfigReader{registry}, ConfigIndex: registry,
		Publisher:   publisher,
		OwnerReader: fakeBackupOwnerReader{owned: map[uuid.UUID]uuid.UUID{repoID: testOrgID()}},
		Logger:      zap.NewNop(),
	})

	registry.repositories[repoID] = &domain.BackupRepository{ID: repoID, OrgID: testOrgID(), Name: "test-repo", Backend: domain.BackupBackendKopia}

	intent := &Intent{
		Op:    "recipe-apply",
		OrgID: testOrgID(),
		Content: map[string]any{
			"id":            recipeID.String(),
			"org_id":        testOrgID().String(),
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

	require.Empty(t, registry.recipes, "a publisher mock cannot supply a signed canonical head to index")
	if publisher.recipeCount != 1 {
		t.Errorf("expected 1 recipe publish, got %d", publisher.recipeCount)
	}
}

func TestBackupIntentHandlerMirrorsOnlyCanonicalConfig(t *testing.T) {
	id := uuid.New()
	canonical := newFakeBackupIntentRegistry()
	index := newFakeBackupIntentRegistry()
	canonical.policies[id] = &domain.BackupPolicy{ID: id, OrgID: testOrgID(), Name: "signed-policy", VerificationMode: domain.BackupVerificationNone}
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{ConfigReader: fakeBackupConfigReader{canonical}, ConfigIndex: index, Logger: zap.NewNop()})
	h.mirrorPublishedConfig(t.Context(), kinds.BackupPolicyRegistry, id, false)
	require.Equal(t, "signed-policy", index.policies[id].Name)
	require.Equal(t, testOrgID(), index.policies[id].OrgID)
	h.mirrorPublishedConfig(t.Context(), kinds.BackupRecipeRegistry, uuid.New(), false)
	require.Empty(t, index.recipes, "missing canonical state cannot advance the SQL index")
}

func TestBackupIntentHandlerDoesNotIndexUnpublishedPolicy(t *testing.T) {
	id := uuid.New()
	registry := newFakeBackupIntentRegistry()
	publisher := &fakeBackupIntentPublisher{err: fmt.Errorf("relay outbox unavailable")}
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, Publisher: publisher, OwnerReader: fakeBackupOwnerReader{}, ConfigReader: fakeBackupConfigReader{registry}, ConfigIndex: registry, Logger: zap.NewNop()})
	intent := &Intent{Op: "policy-apply", OrgID: testOrgID(), Content: map[string]any{
		"id": id.String(), "org_id": testOrgID().String(), "name": "retention", "verification_mode": "none",
	}}
	require.ErrorContains(t, h.HandleIntent(t.Context(), intent), "relay outbox unavailable")
	require.Empty(t, registry.policies, "SQL cannot advance before canonical publication")
}

func TestBackupIntentHandler_Delete(t *testing.T) {
	registry := newFakeBackupIntentRegistry()
	publisher := &fakeBackupIntentPublisher{}
	recipeID := uuid.New()
	h := NewBackupIntentHandler(BackupIntentHandlerConfig{
		Registry:     registry,
		ConfigReader: fakeBackupConfigReader{registry}, ConfigIndex: registry,
		Publisher:   publisher,
		OwnerReader: fakeBackupOwnerReader{owned: map[uuid.UUID]uuid.UUID{recipeID: testOrgID()}},
		Logger:      zap.NewNop(),
	})

	registry.recipes[recipeID] = &domain.BackupRecipe{ID: recipeID, OrgID: testOrgID(), Name: "to-delete", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: uuid.New(), TargetRef: "/"}

	intent := &Intent{
		Op:    "delete",
		OrgID: testOrgID(),
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
	workflowCreates  int
	recipes          map[uuid.UUID]*domain.BackupRecipe
	policies         map[uuid.UUID]*domain.BackupPolicy
	repositories     map[uuid.UUID]*domain.BackupRepository
	definitions      map[uuid.UUID]*domain.BackupDefinition
	deleted          map[uuid.UUID]bool
	runs             map[uuid.UUID]*domain.BackupRun
	restores         map[uuid.UUID]*domain.BackupRestoreRun
	verifications    map[uuid.UUID]*domain.BackupVerificationRecord
	retentionRuns    map[uuid.UUID]*domain.BackupRetentionRun
	restoreApprovals int
}

func newFakeBackupIntentRegistry() *fakeBackupIntentRegistry {
	return &fakeBackupIntentRegistry{
		recipes:       make(map[uuid.UUID]*domain.BackupRecipe),
		policies:      make(map[uuid.UUID]*domain.BackupPolicy),
		repositories:  make(map[uuid.UUID]*domain.BackupRepository),
		definitions:   make(map[uuid.UUID]*domain.BackupDefinition),
		deleted:       make(map[uuid.UUID]bool),
		runs:          make(map[uuid.UUID]*domain.BackupRun),
		restores:      make(map[uuid.UUID]*domain.BackupRestoreRun),
		verifications: make(map[uuid.UUID]*domain.BackupVerificationRecord),
		retentionRuns: make(map[uuid.UUID]*domain.BackupRetentionRun),
	}
}

func (r *fakeBackupIntentRegistry) UpsertBackupRecipe(ctx context.Context, v *domain.BackupRecipe) error {
	r.deleted[v.ID] = false
	return r.CreateOrUpdateRecipe(ctx, v)
}
func (r *fakeBackupIntentRegistry) UpsertBackupPolicy(ctx context.Context, v *domain.BackupPolicy) error {
	r.deleted[v.ID] = false
	return r.CreateOrUpdatePolicy(ctx, v)
}
func (r *fakeBackupIntentRegistry) UpsertBackupRepository(ctx context.Context, v *domain.BackupRepository) error {
	r.deleted[v.ID] = false
	return r.CreateOrUpdateRepository(ctx, v)
}
func (r *fakeBackupIntentRegistry) UpsertBackupDefinition(_ context.Context, v *domain.BackupDefinition) error {
	r.deleted[v.ID] = false
	r.definitions[v.ID] = v
	return nil
}
func (r *fakeBackupIntentRegistry) MarkBackupConfigDeleted(_ context.Context, family int, id uuid.UUID) error {
	r.deleted[id] = true
	return nil
}
func (r *fakeBackupIntentRegistry) ResetBackupConfigIndex(context.Context) error {
	for id := range r.repositories {
		r.deleted[id] = true
	}
	for id := range r.policies {
		r.deleted[id] = true
	}
	for id := range r.recipes {
		r.deleted[id] = true
	}
	for id := range r.definitions {
		r.deleted[id] = true
	}
	return nil
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
	r.workflowCreates++
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
	r.workflowCreates++
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
	r.workflowCreates++
	r.retentionRuns[run.ID] = run
	return run, true, nil
}

type fakeBackupIntentPublisher struct {
	err             error
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
	if p.err != nil {
		return p.err
	}
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

func TestBackupIntentHandler_RestoreApprovalPausedBeforeMutation(t *testing.T) {
	for _, source := range []string{"sql-only", "signed-request"} {
		t.Run(source, func(t *testing.T) {
			id := uuid.New()
			registry := newFakeBackupIntentRegistry()
			restore := &domain.BackupRestoreRun{
				ID: id, ApprovalStatus: domain.BackupApprovalPending,
				Metadata: map[string]any{"kopia_restore_source": "sql-overridden-source", "velero_backup_name": "sql-overridden-name"},
			}
			if source == "signed-request" {
				restore.RequestEventID = strings.Repeat("a", 64)
				restore.RequestKind = int(KindBackupRestoreRequest)
			}
			registry.restores[id] = restore
			handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, Logger: zap.NewNop()})
			for _, decision := range []string{"approve", "reject"} {
				intent := &Intent{Domain: "backup", Op: "restore-approval", OrgID: testOrgID(), Actor: testPubkey, IntentID: "approval-" + decision, Coordinate: id.String(), Content: map[string]any{"restore_id": id.String(), "decision": decision}}
				require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "approval paused")
				require.Zero(t, registry.restoreApprovals)
				require.Equal(t, domain.BackupApprovalPending, restore.ApprovalStatus)
			}
		})
	}
}
