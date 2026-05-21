package nostr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestProjectorPublishesBackupSnapshotReadModelsWithRestoreEligibility(t *testing.T) {
	ctx := context.Background()
	backupSource, runID := backupProjectionFixture(domain.RunStatusSucceeded, domain.BackupVerificationSkipped, false)
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithBackupProjectionSource(backupSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	for _, kind := range []int{KindBackupRecipeRegistry, KindBackupPolicyRegistry, KindBackupRepositoryRegistry, KindBackupRunState, KindBackupRestoreState, KindBackupVerificationState} {
		if got := sink.byKind(kind); len(got) != 1 {
			t.Fatalf("kind %d events = %d, want 1", kind, len(got))
		}
	}
	runEvents := sink.byKind(KindBackupRunState)
	assertProjectionTag(t, runEvents[0].Tags, "d", "backup-run:"+runID.String())
	assertProjectionTag(t, runEvents[0].Tags, "restore_eligible", "false")
	var payload map[string]any
	if err := json.Unmarshal([]byte(runEvents[0].Content), &payload); err != nil {
		t.Fatalf("decode run state: %v", err)
	}
	if payload["restore_eligible"] != false {
		t.Fatalf("restore_eligible = %#v, want false for skipped verification", payload["restore_eligible"])
	}
	if payload["verification_status"] != string(domain.BackupVerificationSkipped) {
		t.Fatalf("verification_status = %#v", payload["verification_status"])
	}
}

func TestProjectorBackupMutationPublishesChangedRestore(t *testing.T) {
	ctx := context.Background()
	backupSource, _ := backupProjectionFixture(domain.RunStatusSucceeded, domain.BackupVerificationSucceeded, true)
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithBackupProjectionSource(backupSource))

	projector.handleEvent(ctx, events.Event{Type: service.EventBackupRestoreChanged, EntityID: backupSource.restore.ID.String(), Data: map[string]any{"restore_id": backupSource.restore.ID.String()}})

	restoreEvents := sink.byKind(KindBackupRestoreState)
	if len(restoreEvents) != 1 {
		t.Fatalf("restore state events = %d, want 1", len(restoreEvents))
	}
	assertProjectionTag(t, restoreEvents[0].Tags, "d", "backup-restore:"+backupSource.restore.ID.String())
	assertProjectionTag(t, restoreEvents[0].Tags, "approval", string(domain.BackupApprovalApproved))
	var payload map[string]any
	if err := json.Unmarshal([]byte(restoreEvents[0].Content), &payload); err != nil {
		t.Fatalf("decode restore state: %v", err)
	}
	if payload["backup_run_id"] != backupSource.run.ID.String() || payload["restore_target_ref"] != "fs:/restore" {
		t.Fatalf("unexpected restore payload: %#v", payload)
	}
}

func TestProjectorBackupMutationPublishesChangedRunAndVerification(t *testing.T) {
	ctx := context.Background()
	backupSource, runID := backupProjectionFixture(domain.RunStatusSucceeded, domain.BackupVerificationSucceeded, true)
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithBackupProjectionSource(backupSource))

	projector.handleEvent(ctx, events.Event{Type: service.EventBackupVerificationChanged, EntityID: backupSource.verification.ID.String(), Data: map[string]any{"run_id": runID.String()}})

	if got := sink.byKind(KindBackupVerificationState); len(got) != 1 {
		t.Fatalf("verification state events = %d, want 1", len(got))
	}
	runEvents := sink.byKind(KindBackupRunState)
	if len(runEvents) != 1 {
		t.Fatalf("run state events = %d, want 1", len(runEvents))
	}
	assertProjectionTag(t, runEvents[0].Tags, "restore_eligible", "true")
	var payload map[string]any
	if err := json.Unmarshal([]byte(runEvents[0].Content), &payload); err != nil {
		t.Fatalf("decode run state: %v", err)
	}
	if payload["restore_eligible"] != true {
		t.Fatalf("restore_eligible = %#v, want true for successful verification", payload["restore_eligible"])
	}
}

type fakeBackupProjectionSource struct {
	recipe       domain.BackupRecipe
	policy       domain.BackupPolicy
	repository   domain.BackupRepository
	run          domain.BackupRun
	verification domain.BackupVerificationRecord
	restore      domain.BackupRestoreRun
}

func backupProjectionFixture(status domain.DeploymentRunStatus, verificationStatus domain.BackupVerificationStatus, verified bool) (*fakeBackupProjectionSource, uuid.UUID) {
	now := time.Now().UTC()
	repoID := uuid.New()
	policyID := uuid.New()
	recipeID := uuid.New()
	runID := uuid.New()
	verificationID := uuid.New()
	restoreID := uuid.New()
	return &fakeBackupProjectionSource{
		repository:   domain.BackupRepository{ID: repoID, Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary", CreatedAt: now, UpdatedAt: now},
		policy:       domain.BackupPolicy{ID: policyID, Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, CreatedAt: now, UpdatedAt: now},
		recipe:       domain.BackupRecipe{ID: recipeID, Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, PolicyID: &policyID, TargetRef: "fs:/srv/data", CreatedAt: now, UpdatedAt: now},
		run:          domain.BackupRun{ID: runID, RecipeID: recipeID, RepositoryID: repoID, PolicyID: &policyID, RequestedBy: "requester", RequestEventID: "event", RequestKind: 38400, RequestDTag: "backup:daily", Status: status, Backend: domain.BackupBackendKopia, TargetRef: "fs:/srv/data", SnapshotCreated: true, SnapshotID: "snap-1", VerificationStatus: verificationStatus, CreatedAt: now, UpdatedAt: now},
		verification: domain.BackupVerificationRecord{ID: verificationID, BackupRunID: runID, Mode: domain.BackupVerificationKopiaSnapshotVerify, Status: verificationStatus, Verified: verified, CreatedAt: now, UpdatedAt: now},
		restore:      domain.BackupRestoreRun{ID: restoreID, BackupRunID: runID, RecipeID: recipeID, RepositoryID: repoID, PolicyID: &policyID, SnapshotID: "snap-1", RestoreTargetRef: "fs:/restore", RequestedBy: "requester", RequestEventID: "restore-event", RequestKind: 38402, RequestDTag: "restore:daily", ApprovalStatus: domain.BackupApprovalApproved, ApprovalEventID: "approval-event", ApprovedBy: "operator", Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, VerificationStatus: domain.BackupVerificationSkipped, CreatedAt: now, UpdatedAt: now},
	}, runID
}

func (s *fakeBackupProjectionSource) ListRecipes(context.Context, int, int) ([]domain.BackupRecipe, error) {
	return []domain.BackupRecipe{s.recipe}, nil
}
func (s *fakeBackupProjectionSource) GetRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	if id == s.recipe.ID {
		return &s.recipe, nil
	}
	return nil, nil
}
func (s *fakeBackupProjectionSource) ListPolicies(context.Context, int, int) ([]domain.BackupPolicy, error) {
	return []domain.BackupPolicy{s.policy}, nil
}
func (s *fakeBackupProjectionSource) GetPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	if id == s.policy.ID {
		return &s.policy, nil
	}
	return nil, nil
}
func (s *fakeBackupProjectionSource) ListRepositories(context.Context, int, int) ([]domain.BackupRepository, error) {
	return []domain.BackupRepository{s.repository}, nil
}
func (s *fakeBackupProjectionSource) GetRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	if id == s.repository.ID {
		return &s.repository, nil
	}
	return nil, nil
}
func (s *fakeBackupProjectionSource) ListBackupRuns(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRun, error) {
	return []domain.BackupRun{s.run}, nil
}
func (s *fakeBackupProjectionSource) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	if id == s.run.ID {
		return &s.run, nil
	}
	return nil, nil
}
func (s *fakeBackupProjectionSource) ListBackupRestores(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRestoreRun, error) {
	return []domain.BackupRestoreRun{s.restore}, nil
}
func (s *fakeBackupProjectionSource) GetBackupRestore(_ context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	if id == s.restore.ID {
		return &s.restore, nil
	}
	return nil, nil
}
func (s *fakeBackupProjectionSource) GetBackupVerificationByRunID(_ context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	if runID == s.verification.BackupRunID {
		return &s.verification, nil
	}
	return nil, nil
}

func assertProjectionTag(t *testing.T, tags gonostr.Tags, key, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing projection tag %s=%s in %#v", key, value, tags)
}
