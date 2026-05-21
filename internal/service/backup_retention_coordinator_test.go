package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBackupRetentionCoordinatorProcessOnceCompletesBackendNativeRetention(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupRetentionCoordinatorFixture(t)
	backend := &recordingRetentionBackend{result: &BackupRetentionResult{Evidence: map[string]any{"retained": float64(24), "deleted": float64(3)}}}
	responder := &recordingRetentionResponder{}
	coordinator := NewBackupRetentionCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRetentionResponder(responder), WithBackupRetentionHealthCheck(true))

	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRetentionRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, float64(3), stored.Evidence["deleted"])
	require.Equal(t, "keep-hourly-24", stored.Metadata["retention_selector"])
	require.NotNil(t, stored.FinishedAt)
	require.Equal(t, []string{"health", "retention"}, backend.calls)
	require.Len(t, backend.requests, 1)
	require.True(t, backend.requests[0].Run.DryRun)
	require.Equal(t, run.ID, backend.requests[0].Run.ID)
	require.NotNil(t, backend.requests[0].Policy)
	require.Equal(t, []string{"enforcing_retention"}, responder.statusSteps)
	require.Len(t, responder.results, 1)
	require.Equal(t, domain.RunStatusSucceeded, responder.results[0].run.Status)
}

func TestBackupRetentionCoordinatorUnsupportedCapabilityFailsExplicitly(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupRetentionCoordinatorFixture(t)
	backend := &recordingBackupBackend{}
	coordinator := NewBackupRetentionCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRetentionHealthCheck(false))

	err := coordinator.ProcessBackupRetentionRun(ctx, run.ID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendUnsupported)
	stored, getErr := repo.GetBackupRetentionRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Contains(t, stored.Error, "does not support retention enforcement")
}

func TestBackupRetentionCoordinatorConfigurationFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "no-retention", VerificationMode: domain.BackupVerificationNone}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, backupRepo))
	require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))
	run := validBackupRetentionRun(backupRepo, policy, "event-1")
	require.NoError(t, repo.UpsertBackupRetentionRun(ctx, run))
	backend := &recordingRetentionBackend{result: &BackupRetentionResult{Evidence: map[string]any{"should_not_run": true}}}
	coordinator := NewBackupRetentionCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRetentionHealthCheck(false))

	err := coordinator.ProcessBackupRetentionRun(ctx, run.ID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
	stored, getErr := repo.GetBackupRetentionRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Empty(t, backend.calls)
}

func TestBackupRetentionCoordinatorCancellationLeavesRunRecoverable(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupRetentionCoordinatorFixture(t)
	backend := &recordingRetentionBackend{retentionErr: context.Canceled}
	coordinator := NewBackupRetentionCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRetentionHealthCheck(false))

	err := coordinator.ProcessBackupRetentionRun(ctx, run.ID)

	require.ErrorIs(t, err, context.Canceled)
	stored, getErr := repo.GetBackupRetentionRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusRunning, stored.Status)
	require.Nil(t, stored.FinishedAt)
}

func TestBackupRetentionCoordinatorRequeuesStaleRunningRun(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupRetentionCoordinatorFixture(t)
	old := time.Now().UTC().Add(-time.Hour)
	repo.mu.Lock()
	repo.retentions[run.ID].Status = domain.RunStatusRunning
	repo.retentions[run.ID].StartedAt = &old
	repo.retentions[run.ID].UpdatedAt = old
	repo.mu.Unlock()
	backend := &recordingRetentionBackend{result: &BackupRetentionResult{Evidence: map[string]any{"deleted": float64(0)}}}
	coordinator := NewBackupRetentionCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRetentionHealthCheck(false), WithBackupRetentionCoordinatorConfig(BackupRetentionCoordinatorConfig{StaleRunTimeout: time.Minute}))

	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRetentionRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, true, stored.Metadata["lease_recovered"])
	require.Equal(t, []string{"retention"}, backend.calls)
}

func backupRetentionCoordinatorFixture(t *testing.T) (*BackupRegistryService, *memoryBackupControlPlaneRepository, *domain.BackupRetentionRun) {
	t.Helper()
	ctx := context.Background()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	backupRepo, policy := retentionRegistryFixture(t, ctx, registry)
	run := validBackupRetentionRun(backupRepo, policy, "event-1")
	created, wasCreated, err := registry.CreateBackupRetentionRunIfAbsent(ctx, run)
	require.NoError(t, err)
	require.True(t, wasCreated)
	return registry, repo, created
}

type recordingRetentionBackend struct {
	healthErr    error
	result       *BackupRetentionResult
	retentionErr error
	calls        []string
	requests     []BackupRetentionRequest
}

func (b *recordingRetentionBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}

func (b *recordingRetentionBackend) Health(context.Context, *domain.BackupRepository) error {
	b.calls = append(b.calls, "health")
	return b.healthErr
}

func (b *recordingRetentionBackend) EnforceRetention(_ context.Context, req BackupRetentionRequest) (*BackupRetentionResult, error) {
	b.calls = append(b.calls, "retention")
	b.requests = append(b.requests, req)
	return b.result, b.retentionErr
}

type recordingRetentionResponder struct {
	statusSteps []string
	results     []retentionResponderResult
}

type retentionResponderResult struct {
	run     *domain.BackupRetentionRun
	message string
}

func (r *recordingRetentionResponder) PublishBackupRetentionStatus(_ context.Context, _ *domain.BackupRetentionRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}

func (r *recordingRetentionResponder) PublishBackupRetentionResult(_ context.Context, run *domain.BackupRetentionRun, message string) error {
	r.results = append(r.results, retentionResponderResult{run: run, message: message})
	return nil
}

var _ BackupRetentionBackend = (*recordingRetentionBackend)(nil)
var _ BackupRetentionResponder = (*recordingRetentionResponder)(nil)
