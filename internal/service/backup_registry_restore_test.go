package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBackupRegistryCreateRestoreIfAbsentRequiresRestoreEligibleSourceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, nil)
	publisher := &recordingPublisher{}
	registry.publisher = publisher

	request := validRestoreRequest(sourceRun, "restore-event-1")
	created, wasCreated, err := registry.CreateBackupRestoreIfAbsent(ctx, request)
	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, sourceRun.ID, created.BackupRunID)
	require.Equal(t, sourceRun.RecipeID, created.RecipeID)
	require.Equal(t, sourceRun.RepositoryID, created.RepositoryID)
	require.Equal(t, sourceRun.PolicyID, created.PolicyID)
	require.Equal(t, sourceRun.SnapshotID, created.SnapshotID)
	require.Equal(t, sourceRun.Backend, created.Backend)
	require.Equal(t, domain.BackupApprovalPending, created.ApprovalStatus)
	require.Len(t, publisher.eventsOfType(EventBackupRestoreChanged), 1)

	duplicate := validRestoreRequest(sourceRun, "restore-event-2")
	duplicate.ID = uuid.New()
	existing, wasCreated, err := registry.CreateBackupRestoreIfAbsent(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, created.ID, existing.ID)
	require.Equal(t, "restore-event-1", existing.RequestEventID)
	require.Len(t, repo.restores, 1)
	require.Len(t, publisher.eventsOfType(EventBackupRestoreChanged), 1)
}

func TestBackupRegistryCreateRestoreRejectsSourceThatIsNotRestoreEligible(t *testing.T) {
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, nil)
	sourceRun.VerificationStatus = domain.BackupVerificationSkipped
	require.NoError(t, repo.UpsertBackupRun(ctx, sourceRun))

	_, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-1"))

	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrInvalidValue)
	require.Empty(t, repo.restores)
}

func TestBackupRegistryRestoreApprovalTransitionsAndRejectionTerminalizes(t *testing.T) {
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, nil)
	created, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-1"))
	require.NoError(t, err)

	rejected, changed, err := registry.ApplyBackupRestoreApproval(ctx, created.ID, false, "approval-event-1", "operator-pubkey", "unsafe target")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, domain.BackupApprovalRejected, rejected.ApprovalStatus)
	require.Equal(t, domain.RunStatusFailed, rejected.Status)
	require.Contains(t, rejected.Error, "unsafe target")

	claimed, err := repo.ClaimNextQueuedBackupRestore(ctx)
	require.NoError(t, err)
	require.Nil(t, claimed)

	late, changed, err := registry.ApplyBackupRestoreApproval(ctx, created.ID, true, "approval-event-2", "operator-pubkey", "late approval")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, domain.BackupApprovalRejected, late.ApprovalStatus)
}

func TestBackupRegistryRestoreCanSkipApprovalWhenPolicyAllows(t *testing.T) {
	ctx := context.Background()
	policyMetadata := map[string]any{BackupPolicyMetadataRestoreApprovalRequired: false}
	registry, _, sourceRun := restoreRegistryFixture(t, policyMetadata)

	created, wasCreated, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-1"))

	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, domain.BackupApprovalNotRequired, created.ApprovalStatus)
	require.Equal(t, domain.RunStatusQueued, created.Status)
}

func TestBackupRegistryCompleteRestoreIsIdempotentAfterTerminalState(t *testing.T) {
	ctx := context.Background()
	registry, _, sourceRun := restoreRegistryFixture(t, nil)
	created, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-terminal"))
	require.NoError(t, err)
	approved, changed, err := registry.ApplyBackupRestoreApproval(ctx, created.ID, true, "approval-event-1", "operator-pubkey", "approved")
	require.NoError(t, err)
	require.True(t, changed)
	completed, err := registry.CompleteBackupRestore(ctx, approved.ID, &BackupRestoreResult{Evidence: map[string]any{"restored": true}}, nil)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, completed.Status)

	late, err := registry.CompleteBackupRestore(ctx, approved.ID, nil, context.Canceled)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, late.Status)
	require.Empty(t, late.Error)
	require.Equal(t, map[string]any{"restored": true}, late.Evidence)
}

func TestBackupRegistryCompleteRestoreFailsClosedWhenPolicyRequiresVerification(t *testing.T) {
	ctx := context.Background()
	policyMetadata := map[string]any{BackupPolicyMetadataRestoreVerificationMode: string(domain.BackupVerificationKopiaSnapshotVerify)}
	registry, _, sourceRun := restoreRegistryFixture(t, policyMetadata)
	created, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-1"))
	require.NoError(t, err)
	approved, changed, err := registry.ApplyBackupRestoreApproval(ctx, created.ID, true, "approval-event-1", "operator-pubkey", "approved")
	require.NoError(t, err)
	require.True(t, changed)

	completed, err := registry.CompleteBackupRestore(ctx, approved.ID, &BackupRestoreResult{Verified: false, VerificationStatus: domain.BackupVerificationFailed, Evidence: map[string]any{"checked": false}, Error: "restore checksum mismatch"}, nil)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, completed.Status)
	require.Equal(t, domain.BackupVerificationFailed, completed.VerificationStatus)
	require.Equal(t, map[string]any{"checked": false}, completed.Evidence)
	require.Contains(t, completed.Error, "restore checksum mismatch")
}

func restoreRegistryFixture(t *testing.T, policyMetadata map[string]any) (*BackupRegistryService, *memoryBackupControlPlaneRepository, *domain.BackupRun) {
	t.Helper()
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	repositoryRecord := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, repositoryRecord))
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "restore-policy", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, Metadata: policyMetadata}
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repositoryRecord.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data", VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	require.NoError(t, registry.CreateOrUpdateRecipe(ctx, recipe))
	run := &domain.BackupRun{ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: repositoryRecord.ID, PolicyID: &policy.ID, RequestedBy: "pubkey", RequestEventID: "backup-event", RequestKind: 38400, RequestDTag: "daily:/srv/data", Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, TargetRef: recipe.TargetRef, SnapshotCreated: true, SnapshotID: "snapshot-verified", VerificationStatus: domain.BackupVerificationSucceeded}
	require.NoError(t, registry.CreateOrUpdateBackupRun(ctx, run))
	return registry, repo, run
}

func validRestoreRequest(sourceRun *domain.BackupRun, eventID string) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{
		ID:               uuid.New(),
		BackupRunID:      sourceRun.ID,
		RestoreTargetRef: "fs:/restore/path",
		RequestedBy:      "restore-requester",
		RequestEventID:   eventID,
		RequestKind:      38402,
		RequestDTag:      "restore:daily:/srv/data",
		Metadata:         map[string]any{"reason": "operator-request"},
	}
}
