package service

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestStaticBackupBackendResolverResolvesByKind(t *testing.T) {
	backend := &recordingBackupBackend{}
	resolver, err := NewStaticBackupBackendResolver(backend)

	require.NoError(t, err)
	resolved, ok := resolver.Resolve(domain.BackupBackendKopia)
	require.True(t, ok)
	require.Same(t, backend, resolved)
	require.Equal(t, []domain.BackupBackendKind{domain.BackupBackendKopia}, resolver.Kinds())
}

func TestStaticBackupBackendResolverRejectsDuplicateKind(t *testing.T) {
	_, err := NewStaticBackupBackendResolver(&recordingBackupBackend{}, &recordingBackupBackend{})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
}

func TestBackupRunCoordinatorFailsClosedWhenBackendLacksSnapshotCapability(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, false)
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(baseOnlyBackupBackend{}), nil, WithBackupRunHealthCheck(false))

	err := coordinator.ProcessBackupRun(ctx, run.ID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendUnsupported)
	stored, getErr := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Contains(t, stored.Error, "does not support snapshot execution")
}

type baseOnlyBackupBackend struct{}

func (baseOnlyBackupBackend) BackendKind() domain.BackupBackendKind                  { return domain.BackupBackendKopia }
func (baseOnlyBackupBackend) Health(context.Context, *domain.BackupRepository) error { return nil }
