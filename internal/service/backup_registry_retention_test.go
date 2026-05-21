package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBackupRegistryCreateRetentionRunIfAbsentIsIdempotentByRequestCoordinate(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	publisher := &recordingPublisher{}
	registry := NewBackupRegistryService(repo, publisher, nil)
	backupRepo, policy := retentionRegistryFixture(t, ctx, registry)

	first := validBackupRetentionRun(backupRepo, policy, "event-1")
	created, wasCreated, err := registry.CreateBackupRetentionRunIfAbsent(ctx, first)
	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, domain.BackupBackendKopia, created.Backend)
	require.Equal(t, "keep-hourly-24", created.Metadata["retention_selector"])

	duplicate := validBackupRetentionRun(backupRepo, policy, "event-2")
	duplicate.ID = uuid.New()
	existing, wasCreated, err := registry.CreateBackupRetentionRunIfAbsent(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, created.ID, existing.ID)
	require.Equal(t, "event-1", existing.RequestEventID)
	require.Len(t, repo.retentions, 1)
	require.Len(t, publisher.eventsOfType(EventBackupRetentionChanged), 1)
}

func TestBackupRegistryRetentionMissingBackendNativeMetadataFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "missing-retention-metadata", VerificationMode: domain.BackupVerificationNone}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, backupRepo))
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))

	_, _, err := registry.CreateBackupRetentionRunIfAbsent(ctx, validBackupRetentionRun(backupRepo, policy, "event-1"))

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
	require.Empty(t, repo.retentions)
}

func TestBackupRegistryRetentionInvalidMetadataFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "invalid-retention-metadata", VerificationMode: domain.BackupVerificationNone, Metadata: map[string]any{
		BackupPolicyMetadataRetentionMode:          string(BackupRetentionModeBackendNative),
		BackupPolicyMetadataRetentionSelector:      "keep-hourly-24",
		BackupPolicyMetadataRetentionDryRunDefault: "yes",
	}}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, backupRepo))
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))

	_, _, err := registry.CreateBackupRetentionRunIfAbsent(ctx, validBackupRetentionRun(backupRepo, policy, "event-1"))

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
	require.Empty(t, repo.retentions)
}

func TestBackupRegistryCompleteRetentionRunDoesNotReevaluateMutablePolicyMetadata(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	backupRepo, policy := retentionRegistryFixture(t, ctx, registry)
	created, wasCreated, err := registry.CreateBackupRetentionRunIfAbsent(ctx, validBackupRetentionRun(backupRepo, policy, "event-1"))
	require.NoError(t, err)
	require.True(t, wasCreated)
	policy.Metadata = map[string]any{BackupPolicyMetadataRetentionMode: "bahia_computed_delete_list"}
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))

	completed, err := registry.CompleteBackupRetentionRun(ctx, created.ID, map[string]any{"backend_confirmed": true}, nil)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, completed.Status)
	require.Equal(t, true, completed.Evidence["backend_confirmed"])
}

func TestBackupRegistryCompleteRetentionRunPersistsEvidence(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	publisher := &recordingPublisher{}
	registry := NewBackupRegistryService(repo, publisher, nil)
	backupRepo, policy := retentionRegistryFixture(t, ctx, registry)
	created, wasCreated, err := registry.CreateBackupRetentionRunIfAbsent(ctx, validBackupRetentionRun(backupRepo, policy, "event-1"))
	require.NoError(t, err)
	require.True(t, wasCreated)

	completed, err := registry.CompleteBackupRetentionRun(ctx, created.ID, map[string]any{"deleted_snapshots": float64(2), "backend_selector": "keep-hourly-24"}, nil)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, completed.Status)
	require.Equal(t, float64(2), completed.Evidence["deleted_snapshots"])
	require.NotNil(t, completed.FinishedAt)
	stored, err := repo.GetBackupRetentionRun(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, completed.Evidence, stored.Evidence)
	require.Len(t, publisher.eventsOfType(EventBackupRetentionChanged), 2)
}

func retentionRegistryFixture(t *testing.T, ctx context.Context, registry *BackupRegistryService) (*domain.BackupRepository, *domain.BackupPolicy) {
	t.Helper()
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "backend-native-retention", VerificationMode: domain.BackupVerificationNone, Metadata: map[string]any{
		BackupPolicyMetadataRetentionMode:          string(BackupRetentionModeBackendNative),
		BackupPolicyMetadataRetentionSelector:      " keep-hourly-24 ",
		BackupPolicyMetadataRetentionDryRunDefault: true,
	}}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, backupRepo))
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))
	return backupRepo, policy
}

func validBackupRetentionRun(backupRepo *domain.BackupRepository, policy *domain.BackupPolicy, eventID string) *domain.BackupRetentionRun {
	return &domain.BackupRetentionRun{
		ID:             uuid.New(),
		RepositoryID:   backupRepo.ID,
		PolicyID:       &policy.ID,
		RequestedBy:    "pubkey-1",
		RequestEventID: eventID,
		RequestKind:    38404,
		RequestDTag:    "retention:primary",
		Status:         domain.RunStatusQueued,
		Backend:        backupRepo.Backend,
		DryRun:         true,
	}
}
