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

func TestStaticBackupBackendResolverRejectsUntruthfulCapabilityDeclarations(t *testing.T) {
	_, err := NewStaticBackupBackendResolver(declaredOnlySnapshotBackend{})
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "snapshot_create declared=true implemented=false")

	_, err = NewStaticBackupBackendResolver(undeclaredSnapshotBackend{})
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "snapshot_create declared=false implemented=true")
}

func TestStaticBackupBackendResolverReportsAndRequiresCapabilities(t *testing.T) {
	backend := &recordingBackupBackend{}
	resolver := MustBackupBackendResolver(backend)

	capabilities, ok := resolver.Capabilities(domain.BackupBackendKopia)
	require.True(t, ok)
	require.True(t, capabilities.SnapshotCreate)
	require.True(t, capabilities.SnapshotVerify)
	require.True(t, capabilities.Probe)
	require.False(t, capabilities.Retention)
	require.True(t, resolver.Supports(domain.BackupBackendKopia, BackupCapabilitySnapshotCreate))
	require.False(t, resolver.Supports(domain.BackupBackendKopia, BackupCapabilityRetention))

	_, err := resolver.RequireCapabilities(domain.BackupBackendKopia, BackupCapabilitySnapshotCreate, BackupCapabilitySnapshotVerify)
	require.NoError(t, err)

	_, err = resolver.RequireCapabilities(domain.BackupBackendKopia, BackupCapabilityRetention)
	require.ErrorIs(t, err, ErrBackupBackendUnsupported)
	require.Contains(t, err.Error(), "retention")

	_, ok = resolver.Capabilities(domain.BackupBackendVelero)
	require.False(t, ok)
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

func (baseOnlyBackupBackend) BackendKind() domain.BackupBackendKind { return domain.BackupBackendKopia }
func (baseOnlyBackupBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{Probe: true}
}
func (baseOnlyBackupBackend) Health(context.Context, *domain.BackupRepository) error { return nil }

type declaredOnlySnapshotBackend struct{}

func (declaredOnlySnapshotBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (declaredOnlySnapshotBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{SnapshotCreate: true, Probe: true}
}
func (declaredOnlySnapshotBackend) Health(context.Context, *domain.BackupRepository) error {
	return nil
}

type undeclaredSnapshotBackend struct{}

func (undeclaredSnapshotBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (undeclaredSnapshotBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{Probe: true}
}
func (undeclaredSnapshotBackend) Health(context.Context, *domain.BackupRepository) error { return nil }
func (undeclaredSnapshotBackend) CreateSnapshot(context.Context, BackupSnapshotRequest) (*BackupSnapshotResult, error) {
	return nil, nil
}
