package service

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBackupRestoreCoordinatorProcessOnceCompletesApprovedRestore(t *testing.T) {
	ctx := context.Background()
	registry, repo, restore := backupRestoreCoordinatorFixture(t, nil)
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{Evidence: map[string]any{"restored_paths": float64(3)}}}
	responder := &recordingRestoreResponder{}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreResponder(responder), WithBackupRestoreHealthCheck(true))

	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, domain.BackupVerificationSkipped, stored.VerificationStatus)
	require.Equal(t, map[string]any{"restored_paths": float64(3)}, stored.Evidence)
	require.Equal(t, []string{"health", "restore"}, backend.calls)
	require.Equal(t, []string{"restoring"}, responder.statusSteps)
	require.Len(t, responder.results, 1)
}

func TestBackupRestoreCoordinatorRejectedRestoreNeverExecutesBackend(t *testing.T) {
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, nil)
	restore, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-rejected"))
	require.NoError(t, err)
	_, changed, err := registry.ApplyBackupRestoreApproval(ctx, restore.ID, false, "approval-reject", "operator", "unsafe target")
	require.NoError(t, err)
	require.True(t, changed)
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{}}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	require.NoError(t, coordinator.ProcessBackupRestore(ctx, restore.ID))
	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupApprovalRejected, stored.ApprovalStatus)
	require.Empty(t, backend.calls)
}

func TestBackupRestoreCoordinatorPendingApprovalDoesNotExecuteBackend(t *testing.T) {
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, nil)
	restore, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-pending"))
	require.NoError(t, err)
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{}}
	responder := &recordingRestoreResponder{}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreResponder(responder), WithBackupRestoreHealthCheck(false))

	require.NoError(t, coordinator.ProcessBackupRestore(ctx, restore.ID))
	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusQueued, stored.Status)
	require.Equal(t, domain.BackupApprovalPending, stored.ApprovalStatus)
	require.Empty(t, backend.calls)
	require.Equal(t, []string{"pending_approval"}, responder.statusSteps)
}

func TestBackupRestoreCoordinatorUnsupportedCapabilityFailsExplicitly(t *testing.T) {
	ctx := context.Background()
	registry, repo, restore := backupRestoreCoordinatorFixture(t, nil)
	backend := &healthOnlyBackupBackend{}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	err := coordinator.ProcessBackupRestore(ctx, restore.ID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendUnsupported)
	stored, getErr := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Contains(t, stored.Error, "does not support restore execution")
}

func TestBackupRestoreCoordinatorVerificationRequiredFailsClosed(t *testing.T) {
	ctx := context.Background()
	policyMetadata := map[string]any{BackupPolicyMetadataRestoreVerificationMode: string(domain.BackupVerificationKopiaSnapshotVerify)}
	registry, repo, restore := backupRestoreCoordinatorFixture(t, policyMetadata)
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{Verified: false, VerificationStatus: domain.BackupVerificationFailed, Evidence: map[string]any{"verified": false}, Error: "restored data mismatch"}}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	err := coordinator.ProcessBackupRestore(ctx, restore.ID)

	require.Error(t, err)
	stored, getErr := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupVerificationFailed, stored.VerificationStatus)
	require.Contains(t, stored.Error, "restored data mismatch")
	require.Equal(t, []string{"restore"}, backend.calls)
}

func TestBackupRestoreCoordinatorRequiredVerificationGenericBackendErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	policyMetadata := map[string]any{BackupPolicyMetadataRestoreVerificationMode: string(domain.BackupVerificationKopiaSnapshotVerify)}
	registry, repo, restore := backupRestoreCoordinatorFixture(t, policyMetadata)
	backend := &recordingRestoreBackend{restoreErr: errors.New("restore command failed")}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	err := coordinator.ProcessBackupRestore(ctx, restore.ID)

	require.Error(t, err)
	stored, getErr := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupVerificationFailed, stored.VerificationStatus)
	require.Contains(t, stored.Error, "restore command failed")
}

func TestBackupRestoreCoordinatorCancellationLeavesRestoreRecoverable(t *testing.T) {
	ctx := context.Background()
	registry, repo, restore := backupRestoreCoordinatorFixture(t, nil)
	backend := &recordingRestoreBackend{restoreErr: context.Canceled}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	err := coordinator.ProcessBackupRestore(ctx, restore.ID)

	require.ErrorIs(t, err, context.Canceled)
	stored, getErr := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusRunning, stored.Status)
	require.Nil(t, stored.FinishedAt)
}

func TestBackupRestoreCoordinatorProcessOnceRecoversStaleRunningRestore(t *testing.T) {
	ctx := context.Background()
	registry, repo, restore := backupRestoreCoordinatorFixture(t, nil)
	running, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	running.Status = domain.RunStatusRunning
	require.NoError(t, repo.UpsertBackupRestore(ctx, running))
	repo.forceRestoreUpdatedAt(restore.ID, staleBackupTestTime())
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{}}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false), WithBackupRestoreCoordinatorConfig(BackupRestoreCoordinatorConfig{StaleRunTimeout: 1}))

	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, []string{"restore"}, backend.calls)
	require.Equal(t, true, stored.Metadata["lease_recovered"])
}

func TestBackupRestoreCoordinatorRequiredVerificationUnsupportedFailsClosed(t *testing.T) {
	ctx := context.Background()
	policyMetadata := map[string]any{BackupPolicyMetadataRestoreVerificationMode: string(domain.BackupVerificationKopiaSnapshotVerify)}
	registry, repo, restore := backupRestoreCoordinatorFixture(t, policyMetadata)
	backend := &recordingRestoreBackend{restoreErr: errors.Join(ErrBackupBackendUnsupported, errors.New("restore verification unsupported"))}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	err := coordinator.ProcessBackupRestore(ctx, restore.ID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendUnsupported)
	stored, getErr := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupVerificationUnsupported, stored.VerificationStatus)
}

func backupRestoreCoordinatorFixture(t *testing.T, policyMetadata map[string]any) (*BackupRegistryService, *memoryBackupControlPlaneRepository, *domain.BackupRestoreRun) {
	t.Helper()
	ctx := context.Background()
	registry, repo, sourceRun := restoreRegistryFixture(t, policyMetadata)
	restore, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-event-1"))
	require.NoError(t, err)
	approved, changed, err := registry.ApplyBackupRestoreApproval(ctx, restore.ID, true, "approval-event-1", "operator-pubkey", "approved")
	require.NoError(t, err)
	require.True(t, changed)
	return registry, repo, approved
}

type recordingRestoreBackend struct {
	healthErr  error
	result     *BackupRestoreResult
	restoreErr error
	calls      []string
}

func (b *recordingRestoreBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (b *recordingRestoreBackend) Health(context.Context, *domain.BackupRepository) error {
	b.calls = append(b.calls, "health")
	return b.healthErr
}
func (b *recordingRestoreBackend) Restore(context.Context, BackupRestoreRequest) (*BackupRestoreResult, error) {
	b.calls = append(b.calls, "restore")
	return b.result, b.restoreErr
}

type healthOnlyBackupBackend struct{}

func (b *healthOnlyBackupBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (b *healthOnlyBackupBackend) Health(context.Context, *domain.BackupRepository) error {
	return nil
}

type recordingRestoreResponder struct {
	statusSteps []string
	results     []restoreResponderResult
}

type restoreResponderResult struct {
	restore *domain.BackupRestoreRun
	message string
}

func (r *recordingRestoreResponder) PublishBackupRestoreStatus(_ context.Context, _ *domain.BackupRestoreRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}
func (r *recordingRestoreResponder) PublishBackupRestoreResult(_ context.Context, restore *domain.BackupRestoreRun, message string) error {
	r.results = append(r.results, restoreResponderResult{restore: restore, message: message})
	return nil
}
