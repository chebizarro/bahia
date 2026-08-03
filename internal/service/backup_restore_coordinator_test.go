package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestRelayPolicyProjectionBackupRestoreRoundTripPreservesProvenanceAndStaysCached(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, true)
	payload := []byte(`{"schema":"bahia.relay-settings.v1","browser_relays":["wss://relay.example"]}`)
	sum := sha256.Sum256(payload)
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	confirmedAt := now
	projectionStore := &memoryRelayPolicyBackupStore{projection: repository.RelayPolicyProjection{
		AuthorPubkey:     strings.Repeat("a", 64),
		EventID:          strings.Repeat("b", 64),
		EventCreatedAt:   now.Add(-time.Minute),
		EventAcceptedAt:  now,
		Schema:           "bahia.relay-settings.v1",
		CanonicalPayload: payload,
		PayloadHash:      hex.EncodeToString(sum[:]),
		SourceRelay:      "wss://relay.example",
		LastSyncAt:       now,
		RelayConfirmedAt: &confirmedAt,
	}}
	backend := &projectionRoundTripBackupBackend{}
	backupCoordinator := NewBackupRunCoordinator(
		registry,
		MustBackupBackendResolver(backend),
		nil,
		WithBackupRunHealthCheck(false),
		WithRelayPolicyProjectionBackup(projectionStore, projectionStore.projection.AuthorPubkey),
	)
	require.NoError(t, backupCoordinator.ProcessBackupRun(ctx, run.ID))
	sourceRun, err := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, err)
	require.True(t, domain.BackupRunRestoreEligible(sourceRun))
	require.Contains(t, sourceRun.Metadata, "relay_policy_projection_backup")

	restore, _, err := registry.CreateBackupRestoreIfAbsent(ctx, validRestoreRequest(sourceRun, "restore-policy-event"))
	require.NoError(t, err)
	restore, _, err = registry.ApplyBackupRestoreApproval(ctx, restore.ID, true, "approval-policy-event", "operator", "approved")
	require.NoError(t, err)
	restoreCoordinator := NewBackupRestoreCoordinator(
		registry,
		MustBackupBackendResolver(backend),
		nil,
		WithBackupRestoreHealthCheck(false),
		WithRelayPolicyProjectionRestore(projectionStore, projectionStore.projection.AuthorPubkey),
	)
	require.NoError(t, restoreCoordinator.ProcessBackupRestore(ctx, restore.ID))
	require.NotNil(t, projectionStore.restored)
	require.Equal(t, projectionStore.projection.EventID, projectionStore.restored.EventID)
	require.Equal(t, projectionStore.projection.PayloadHash, projectionStore.restored.PayloadHash)
	require.Equal(t, projectionStore.projection.AuthorPubkey, projectionStore.restored.AuthorPubkey)
	require.Equal(t, projectionStore.projection.EventCreatedAt, projectionStore.restored.EventCreatedAt)
	require.Nil(t, projectionStore.restored.RelayConfirmedAt)
}

type memoryRelayPolicyBackupStore struct {
	projection repository.RelayPolicyProjection
	restored   *repository.RelayPolicyProjection
}

func (s *memoryRelayPolicyBackupStore) Export(_ context.Context, author string, exportedAt time.Time) (*repository.RelayPolicyProjectionBackup, error) {
	if author != s.projection.AuthorPubkey {
		return nil, errors.New("unexpected relay policy author")
	}
	backup, err := repository.NewRelayPolicyProjectionBackup(s.projection, exportedAt)
	return &backup, err
}

func (s *memoryRelayPolicyBackupStore) RestoreCached(_ context.Context, backup repository.RelayPolicyProjectionBackup) (bool, error) {
	if err := repository.ValidateRelayPolicyProjectionBackup(backup); err != nil {
		return false, err
	}
	s.restored = &repository.RelayPolicyProjection{
		AuthorPubkey:     backup.AuthorPubkey,
		EventID:          backup.EventID,
		EventCreatedAt:   backup.EventCreatedAt,
		EventAcceptedAt:  backup.EventAcceptedAt,
		Schema:           backup.PolicySchema,
		CanonicalPayload: append([]byte(nil), backup.CanonicalPayload...),
		PayloadHash:      backup.PayloadHash,
		SourceRelay:      backup.SourceRelay,
		LastSyncAt:       backup.LastSyncAt,
		RelayConfirmedAt: nil,
	}
	return true, nil
}

type projectionRoundTripBackupBackend struct{}

func (*projectionRoundTripBackupBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (*projectionRoundTripBackupBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{SnapshotCreate: true, SnapshotVerify: true, Restore: true, Probe: true}
}
func (*projectionRoundTripBackupBackend) Health(context.Context, *domain.BackupRepository) error {
	return nil
}
func (*projectionRoundTripBackupBackend) CreateSnapshot(context.Context, BackupSnapshotRequest) (*BackupSnapshotResult, error) {
	return &BackupSnapshotResult{SnapshotID: "relay-policy-snapshot"}, nil
}
func (*projectionRoundTripBackupBackend) VerifySnapshot(context.Context, BackupVerifyRequest) (*BackupVerifyResult, error) {
	return &BackupVerifyResult{Verified: true, Status: domain.BackupVerificationSucceeded}, nil
}
func (*projectionRoundTripBackupBackend) Restore(context.Context, BackupRestoreRequest) (*BackupRestoreResult, error) {
	return &BackupRestoreResult{}, nil
}

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

func TestBackupRestoreCoordinatorReconcilesExecutedCheckpointWithoutRestoreRerun(t *testing.T) {
	ctx := context.Background()
	registry, repo, restore := backupRestoreCoordinatorFixture(t, nil)
	checkpoint, created, err := repo.StartBackupOperation(ctx, repository.BackupOperationRestore, restore.ID, uuid.New())
	require.NoError(t, err)
	require.True(t, created)
	result, err := json.Marshal(&BackupRestoreResult{Evidence: map[string]any{"restored": true}})
	require.NoError(t, err)
	require.NoError(t, repo.MarkBackupOperationExecuted(ctx, repository.BackupOperationRestore, restore.ID, checkpoint.Token, result))
	backend := &recordingRestoreBackend{result: &BackupRestoreResult{Evidence: map[string]any{"must_not_run": true}}}
	coordinator := NewBackupRestoreCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRestoreHealthCheck(false))

	require.NoError(t, coordinator.ProcessBackupRestore(ctx, restore.ID))
	stored, err := repo.GetBackupRestore(ctx, restore.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, map[string]any{"restored": true}, stored.Evidence)
	require.NotContains(t, backend.calls, "restore")
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
func (b *recordingRestoreBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{Restore: true, Probe: true}
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
func (b *healthOnlyBackupBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{Probe: true}
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
