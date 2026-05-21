package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestBackupRunCoordinatorProcessOnceCompletesSnapshotWithoutVerification(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, false)
	backend := &recordingBackupBackend{snapshot: &BackupSnapshotResult{SnapshotID: "snap-1", Evidence: map[string]any{"bytes": float64(42)}}}
	responder := &recordingBackupResponder{}
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRunResponder(responder), WithBackupRunHealthCheck(true))

	require.NoError(t, coordinator.ProcessOnce(ctx))

	stored, err := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.True(t, stored.SnapshotCreated)
	require.Equal(t, "snap-1", stored.SnapshotID)
	require.Equal(t, domain.BackupVerificationSkipped, stored.VerificationStatus)
	require.False(t, domain.BackupRunRestoreEligible(stored))
	require.Equal(t, []string{"health", "snapshot"}, backend.calls)
	require.Equal(t, []string{"snapshotting"}, responder.statusSteps)
	require.Len(t, responder.results, 1)
	require.Nil(t, responder.results[0].verification)
}

func TestBackupRunCoordinatorVerificationRequiredSucceedsRestoreEligible(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, true)
	backend := &recordingBackupBackend{
		snapshot: &BackupSnapshotResult{SnapshotID: "snap-verified"},
		verify:   &BackupVerifyResult{Verified: true, Status: domain.BackupVerificationSucceeded, Evidence: map[string]any{"checked": true}},
	}
	responder := &recordingBackupResponder{}
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRunResponder(responder), WithBackupRunHealthCheck(false), WithBackupRunCoordinatorConfig(BackupRunCoordinatorConfig{VerifyFilesPercent: 100}))

	require.NoError(t, coordinator.ProcessBackupRun(ctx, run.ID))

	stored, err := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, stored.Status)
	require.Equal(t, domain.BackupVerificationSucceeded, stored.VerificationStatus)
	require.True(t, domain.BackupRunRestoreEligible(stored))
	verification, err := repo.GetBackupVerificationByRunID(ctx, run.ID)
	require.NoError(t, err)
	require.True(t, verification.Verified)
	require.Equal(t, domain.BackupVerificationSucceeded, verification.Status)
	require.Equal(t, []string{"snapshot", "verify"}, backend.calls)
	require.Equal(t, []string{"snapshotting", "verifying"}, responder.statusSteps)
	require.Len(t, responder.results, 1)
	require.Equal(t, domain.BackupVerificationSucceeded, responder.results[0].verification.Status)
}

func TestBackupRunCoordinatorVerificationRequiredFailsClosed(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, true)
	backend := &recordingBackupBackend{
		snapshot: &BackupSnapshotResult{SnapshotID: "snap-failed"},
		verify:   &BackupVerifyResult{Verified: false, Status: domain.BackupVerificationFailed, Evidence: map[string]any{"checked": false}, Error: "corrupt snapshot"},
	}
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRunHealthCheck(false))

	err := coordinator.ProcessBackupRun(ctx, run.ID)

	require.Error(t, err)
	stored, getErr := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupVerificationFailed, stored.VerificationStatus)
	require.False(t, domain.BackupRunRestoreEligible(stored))
	verification, getErr := repo.GetBackupVerificationByRunID(ctx, run.ID)
	require.NoError(t, getErr)
	require.False(t, verification.Verified)
	require.Equal(t, "corrupt snapshot", verification.Error)
}

func TestBackupRunCoordinatorUnsupportedVerificationFailsClosed(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, true)
	backend := &recordingBackupBackend{
		snapshot:  &BackupSnapshotResult{SnapshotID: "snap-unsupported"},
		verifyErr: errors.New("unsupported verification backend"),
	}
	backend.verifyErr = errors.Join(ErrBackupBackendUnsupported, backend.verifyErr)
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRunHealthCheck(false))

	err := coordinator.ProcessBackupRun(ctx, run.ID)

	require.Error(t, err)
	stored, getErr := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Equal(t, domain.BackupVerificationUnsupported, stored.VerificationStatus)
	verification, getErr := repo.GetBackupVerificationByRunID(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.BackupVerificationUnsupported, verification.Status)
	require.False(t, verification.Verified)
}

func TestBackupRunCoordinatorCancellationLeavesRunRecoverable(t *testing.T) {
	ctx := context.Background()
	registry, repo, run := backupCoordinatorFixture(t, false)
	backend := &recordingBackupBackend{snapshotErr: context.Canceled}
	coordinator := NewBackupRunCoordinator(registry, MustBackupBackendResolver(backend), nil, WithBackupRunHealthCheck(false))

	err := coordinator.ProcessBackupRun(ctx, run.ID)

	require.ErrorIs(t, err, context.Canceled)
	stored, getErr := repo.GetBackupRun(ctx, run.ID)
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusRunning, stored.Status)
	require.False(t, stored.SnapshotCreated)
	require.Nil(t, stored.FinishedAt)
}

func backupCoordinatorFixture(t *testing.T, requireVerification bool) (*BackupRegistryService, *memoryBackupControlPlaneRepository, *domain.BackupRun) {
	t.Helper()
	repo := newMemoryBackupControlPlaneRepository()
	registry := NewBackupRegistryService(repo, nil, nil)
	ctx := context.Background()
	repositoryRecord := &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	require.NoError(t, registry.CreateOrUpdateRepository(ctx, repositoryRecord))
	var policyID *uuid.UUID
	if requireVerification {
		policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
		require.NoError(t, registry.CreateOrUpdatePolicy(ctx, policy))
		policyID = &policy.ID
	}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repositoryRecord.ID, PolicyID: policyID, TargetRef: "fs:/srv/data", VerificationMode: domain.BackupVerificationNone}
	require.NoError(t, registry.CreateOrUpdateRecipe(ctx, recipe))
	run := &domain.BackupRun{ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: repositoryRecord.ID, PolicyID: policyID, RequestedBy: "pubkey", RequestEventID: "event", RequestKind: 38400, RequestDTag: "daily:/srv/data", Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, TargetRef: recipe.TargetRef, VerificationStatus: domain.BackupVerificationPending}
	require.NoError(t, registry.CreateOrUpdateBackupRun(ctx, run))
	return registry, repo, run
}

type recordingBackupBackend struct {
	healthErr   error
	snapshot    *BackupSnapshotResult
	snapshotErr error
	verify      *BackupVerifyResult
	verifyErr   error
	calls       []string
}

func (b *recordingBackupBackend) BackendKind() domain.BackupBackendKind {
	return domain.BackupBackendKopia
}
func (b *recordingBackupBackend) Health(context.Context, *domain.BackupRepository) error {
	b.calls = append(b.calls, "health")
	return b.healthErr
}
func (b *recordingBackupBackend) CreateSnapshot(context.Context, BackupSnapshotRequest) (*BackupSnapshotResult, error) {
	b.calls = append(b.calls, "snapshot")
	return b.snapshot, b.snapshotErr
}
func (b *recordingBackupBackend) VerifySnapshot(context.Context, BackupVerifyRequest) (*BackupVerifyResult, error) {
	b.calls = append(b.calls, "verify")
	return b.verify, b.verifyErr
}

type recordingBackupResponder struct {
	statusSteps []string
	results     []backupResponderResult
}

type backupResponderResult struct {
	run          *domain.BackupRun
	verification *domain.BackupVerificationRecord
	message      string
}

func (r *recordingBackupResponder) PublishBackupRunStatus(_ context.Context, _ *domain.BackupRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}
func (r *recordingBackupResponder) PublishBackupRunResult(_ context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord, message string) error {
	r.results = append(r.results, backupResponderResult{run: run, verification: verification, message: message})
	return nil
}

type memoryBackupControlPlaneRepository struct {
	mu            sync.Mutex
	recipes       map[uuid.UUID]*domain.BackupRecipe
	policies      map[uuid.UUID]*domain.BackupPolicy
	repositories  map[uuid.UUID]*domain.BackupRepository
	runs          map[uuid.UUID]*domain.BackupRun
	restores      map[uuid.UUID]*domain.BackupRestoreRun
	retentions    map[uuid.UUID]*domain.BackupRetentionRun
	verifications map[uuid.UUID]*domain.BackupVerificationRecord
}

func newMemoryBackupControlPlaneRepository() *memoryBackupControlPlaneRepository {
	return &memoryBackupControlPlaneRepository{recipes: map[uuid.UUID]*domain.BackupRecipe{}, policies: map[uuid.UUID]*domain.BackupPolicy{}, repositories: map[uuid.UUID]*domain.BackupRepository{}, runs: map[uuid.UUID]*domain.BackupRun{}, restores: map[uuid.UUID]*domain.BackupRestoreRun{}, retentions: map[uuid.UUID]*domain.BackupRetentionRun{}, verifications: map[uuid.UUID]*domain.BackupVerificationRecord{}}
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupRecipe(_ context.Context, recipe *domain.BackupRecipe) error {
	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if recipe.ID == uuid.Nil {
		recipe.ID = uuid.New()
	}
	setBackupTestTimes(&recipe.CreatedAt, &recipe.UpdatedAt)
	cp := *recipe
	r.recipes[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if recipe := r.recipes[id]; recipe != nil {
		cp := *recipe
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRecipeByNameVersion(_ context.Context, name, version string) (*domain.BackupRecipe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, recipe := range r.recipes {
		if recipe.Name == name && recipe.Version == version {
			cp := *recipe
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupRecipes(context.Context, int, int) ([]domain.BackupRecipe, error) {
	return nil, nil
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupPolicy(_ context.Context, policy *domain.BackupPolicy) error {
	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	setBackupTestTimes(&policy.CreatedAt, &policy.UpdatedAt)
	cp := *policy
	r.policies[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if policy := r.policies[id]; policy != nil {
		cp := *policy
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupPolicyByName(_ context.Context, name string) (*domain.BackupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, policy := range r.policies {
		if policy.Name == name {
			cp := *policy
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupPolicies(context.Context, int, int) ([]domain.BackupPolicy, error) {
	return nil, nil
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupRepository(_ context.Context, repo *domain.BackupRepository) error {
	if err := domain.ValidateBackupRepository(repo); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	setBackupTestTimes(&repo.CreatedAt, &repo.UpdatedAt)
	cp := *repo
	r.repositories[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo := r.repositories[id]; repo != nil {
		cp := *repo
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRepositoryByName(_ context.Context, name string) (*domain.BackupRepository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, repo := range r.repositories {
		if repo.Name == name {
			cp := *repo
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupRepositories(context.Context, int, int) ([]domain.BackupRepository, error) {
	return nil, nil
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupRun(_ context.Context, run *domain.BackupRun) error {
	if err := domain.ValidateBackupRun(run); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTestTimes(&run.CreatedAt, &run.UpdatedAt)
	cp := *run
	r.runs[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run := r.runs[id]; run != nil {
		cp := *run
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRunByRequestCoordinate(_ context.Context, pubkey string, kind int, dTag string) (*domain.BackupRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.runs {
		if run.RequestedBy == pubkey && run.RequestKind == kind && run.RequestDTag == dTag {
			cp := *run
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	if existing, _ := r.GetBackupRunByRequestCoordinate(ctx, run.RequestedBy, run.RequestKind, run.RequestDTag); existing != nil {
		return existing, false, nil
	}
	if err := r.UpsertBackupRun(ctx, run); err != nil {
		return nil, false, err
	}
	created, err := r.GetBackupRun(ctx, run.ID)
	return created, true, err
}
func (r *memoryBackupControlPlaneRepository) ClaimNextQueuedBackupRun(_ context.Context) (*domain.BackupRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []uuid.UUID
	for id, run := range r.runs {
		if run.Status == domain.RunStatusQueued {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.runs[ids[i]].CreatedAt.Before(r.runs[ids[j]].CreatedAt) })
	if len(ids) == 0 {
		return nil, nil
	}
	run := r.runs[ids[0]]
	now := time.Now().UTC()
	run.Status = domain.RunStatusRunning
	if run.StartedAt == nil {
		run.StartedAt = &now
	}
	run.UpdatedAt = now
	cp := *run
	return &cp, nil
}
func (r *memoryBackupControlPlaneRepository) RequeueStaleBackupRuns(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupRuns(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRun, error) {
	return nil, nil
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupRestore(_ context.Context, restore *domain.BackupRestoreRun) error {
	if err := domain.ValidateBackupRestoreRun(restore); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if restore.ID == uuid.Nil {
		restore.ID = uuid.New()
	}
	setBackupTestTimes(&restore.CreatedAt, &restore.UpdatedAt)
	cp := *restore
	r.restores[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRestore(_ context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if restore := r.restores[id]; restore != nil {
		cp := *restore
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRestoreByRequestCoordinate(_ context.Context, pubkey string, kind int, dTag string) (*domain.BackupRestoreRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, restore := range r.restores {
		if restore.RequestedBy == pubkey && restore.RequestKind == kind && restore.RequestDTag == dTag {
			cp := *restore
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	if existing, _ := r.GetBackupRestoreByRequestCoordinate(ctx, restore.RequestedBy, restore.RequestKind, restore.RequestDTag); existing != nil {
		return existing, false, nil
	}
	if err := r.UpsertBackupRestore(ctx, restore); err != nil {
		return nil, false, err
	}
	created, err := r.GetBackupRestore(ctx, restore.ID)
	return created, true, err
}
func (r *memoryBackupControlPlaneRepository) ClaimNextQueuedBackupRestore(_ context.Context) (*domain.BackupRestoreRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []uuid.UUID
	for id, restore := range r.restores {
		if restore.Status == domain.RunStatusQueued && (restore.ApprovalStatus == domain.BackupApprovalApproved || restore.ApprovalStatus == domain.BackupApprovalNotRequired) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.restores[ids[i]].CreatedAt.Before(r.restores[ids[j]].CreatedAt) })
	if len(ids) == 0 {
		return nil, nil
	}
	restore := r.restores[ids[0]]
	now := time.Now().UTC()
	restore.Status = domain.RunStatusRunning
	if restore.StartedAt == nil {
		restore.StartedAt = &now
	}
	restore.UpdatedAt = now
	cp := *restore
	return &cp, nil
}
func (r *memoryBackupControlPlaneRepository) RequeueStaleBackupRestores(_ context.Context, olderThan time.Duration) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().UTC().Add(-olderThan)
	count := 0
	for _, restore := range r.restores {
		if restore.Status == domain.RunStatusRunning && restore.UpdatedAt.Before(cutoff) {
			restore.Status = domain.RunStatusQueued
			restore.StartedAt = nil
			if restore.Metadata == nil {
				restore.Metadata = map[string]any{}
			}
			restore.Metadata["lease_recovered"] = true
			restore.UpdatedAt = time.Now().UTC()
			count++
		}
	}
	return count, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupRestores(_ context.Context, status domain.DeploymentRunStatus, _ int, _ int) ([]domain.BackupRestoreRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.BackupRestoreRun{}
	for _, restore := range r.restores {
		if status == "" || restore.Status == status {
			out = append(out, *restore)
		}
	}
	return out, nil
}

func (r *memoryBackupControlPlaneRepository) forceRestoreUpdatedAt(id uuid.UUID, updatedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if restore := r.restores[id]; restore != nil {
		restore.UpdatedAt = updatedAt
	}
}

func staleBackupTestTime() time.Time {
	return time.Now().UTC().Add(-time.Hour)
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupRetentionRun(_ context.Context, run *domain.BackupRetentionRun) error {
	if err := domain.ValidateBackupRetentionRun(run); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTestTimes(&run.CreatedAt, &run.UpdatedAt)
	cp := *run
	r.retentions[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRetentionRun(_ context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run := r.retentions[id]; run != nil {
		cp := *run
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupRetentionRunByRequestCoordinate(_ context.Context, pubkey string, kind int, dTag string) (*domain.BackupRetentionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.retentions {
		if run.RequestedBy == pubkey && run.RequestKind == kind && run.RequestDTag == dTag {
			cp := *run
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	if existing, _ := r.GetBackupRetentionRunByRequestCoordinate(ctx, run.RequestedBy, run.RequestKind, run.RequestDTag); existing != nil {
		return existing, false, nil
	}
	if err := r.UpsertBackupRetentionRun(ctx, run); err != nil {
		return nil, false, err
	}
	created, err := r.GetBackupRetentionRun(ctx, run.ID)
	return created, true, err
}
func (r *memoryBackupControlPlaneRepository) ClaimNextQueuedBackupRetentionRun(_ context.Context) (*domain.BackupRetentionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []uuid.UUID
	for id, run := range r.retentions {
		if run.Status == domain.RunStatusQueued {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.retentions[ids[i]].CreatedAt.Before(r.retentions[ids[j]].CreatedAt) })
	if len(ids) == 0 {
		return nil, nil
	}
	run := r.retentions[ids[0]]
	now := time.Now().UTC()
	run.Status = domain.RunStatusRunning
	if run.StartedAt == nil {
		run.StartedAt = &now
	}
	run.UpdatedAt = now
	cp := *run
	return &cp, nil
}
func (r *memoryBackupControlPlaneRepository) RequeueStaleBackupRetentionRuns(_ context.Context, olderThan time.Duration) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().UTC().Add(-olderThan)
	count := 0
	for _, run := range r.retentions {
		if run.Status == domain.RunStatusRunning && run.UpdatedAt.Before(cutoff) {
			run.Status = domain.RunStatusQueued
			run.StartedAt = nil
			if run.Metadata == nil {
				run.Metadata = map[string]any{}
			}
			run.Metadata["lease_recovered"] = true
			run.UpdatedAt = time.Now().UTC()
			count++
		}
	}
	return count, nil
}
func (r *memoryBackupControlPlaneRepository) ListBackupRetentionRuns(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRetentionRun, error) {
	return nil, nil
}

func (r *memoryBackupControlPlaneRepository) UpsertBackupVerification(_ context.Context, record *domain.BackupVerificationRecord) error {
	if err := domain.ValidateBackupVerificationRecord(record); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	setBackupTestTimes(&record.CreatedAt, &record.UpdatedAt)
	cp := *record
	r.verifications[cp.ID] = &cp
	return nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupVerification(_ context.Context, id uuid.UUID) (*domain.BackupVerificationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if verification := r.verifications[id]; verification != nil {
		cp := *verification
		return &cp, nil
	}
	return nil, nil
}
func (r *memoryBackupControlPlaneRepository) GetBackupVerificationByRunID(_ context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, verification := range r.verifications {
		if verification.BackupRunID == runID {
			cp := *verification
			return &cp, nil
		}
	}
	return nil, nil
}

func setBackupTestTimes(createdAt, updatedAt *time.Time) {
	now := time.Now().UTC()
	if createdAt.IsZero() {
		*createdAt = now
	}
	*updatedAt = now
}

var _ repository.BackupControlPlaneRepository = (*memoryBackupControlPlaneRepository)(nil)
var _ BackupRunQueueRepository = (*memoryBackupControlPlaneRepository)(nil)
var _ BackupRetentionQueueRepository = (*memoryBackupControlPlaneRepository)(nil)
