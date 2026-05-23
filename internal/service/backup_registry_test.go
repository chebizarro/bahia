package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
)

func TestBackupRegistryCreateRunIfAbsentIsIdempotentByRequestCoordinate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	publisher := &recordingPublisher{}
	svc := NewBackupRegistryService(repo, publisher, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data"}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.policies[policy.ID] = policy
	repo.recipes[recipe.ID] = recipe

	first := validBackupRun(recipe, backupRepo, policy, "event-1")
	created, wasCreated, err := svc.CreateBackupRunIfAbsent(ctx, first)
	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, "event-1", created.RequestEventID)

	duplicate := validBackupRun(recipe, backupRepo, policy, "event-2")
	duplicate.ID = uuid.New()
	existing, wasCreated, err := svc.CreateBackupRunIfAbsent(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, created.ID, existing.ID)
	require.Equal(t, "event-1", existing.RequestEventID)
	require.Len(t, repo.runs, 1)
	require.Len(t, publisher.eventsOfType(EventBackupRunChanged), 1)
}

func TestBackupRegistryRejectsRunThatDoesNotMatchAuthoritativeRecipe(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	svc := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data"}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.policies[policy.ID] = policy
	repo.recipes[recipe.ID] = recipe
	mismatched := validBackupRun(recipe, backupRepo, nil, "event-1")

	_, _, err := svc.CreateBackupRunIfAbsent(ctx, mismatched)

	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrInvalidValue)
	require.Empty(t, repo.runs)
}

func TestBackupRegistryRejectsSucceededVerificationWithoutVerifiedEvidence(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	svc := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data"}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.policies[policy.ID] = policy
	repo.recipes[recipe.ID] = recipe
	run := validBackupRun(recipe, backupRepo, policy, "event-1")
	repo.runs[run.ID] = run
	repo.coordinates[coordinate(run.RequestedBy, run.RequestKind, run.RequestDTag)] = run.ID

	_, err := svc.CompleteBackupRun(ctx, run.ID, "snapshot-abc", &domain.BackupVerificationRecord{
		ID:          uuid.New(),
		BackupRunID: run.ID,
		Mode:        domain.BackupVerificationKopiaSnapshotVerify,
		Status:      domain.BackupVerificationSucceeded,
		Verified:    false,
	}, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrInvalidValue)
	require.Empty(t, repo.verifications)
}

func TestBackupRegistryCompleteRunFailsClosedWhenRequiredVerificationFails(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	publisher := &recordingPublisher{}
	svc := NewBackupRegistryService(repo, publisher, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data"}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.policies[policy.ID] = policy
	repo.recipes[recipe.ID] = recipe
	run := validBackupRun(recipe, backupRepo, policy, "event-1")
	repo.runs[run.ID] = run
	repo.coordinates[coordinate(run.RequestedBy, run.RequestKind, run.RequestDTag)] = run.ID

	completed, err := svc.CompleteBackupRun(ctx, run.ID, "snapshot-abc", &domain.BackupVerificationRecord{
		ID:          uuid.New(),
		BackupRunID: run.ID,
		Mode:        domain.BackupVerificationKopiaSnapshotVerify,
		Status:      domain.BackupVerificationFailed,
		Verified:    false,
		Error:       "checksum mismatch",
	}, nil)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, completed.Status)
	require.Equal(t, domain.BackupVerificationFailed, completed.VerificationStatus)
	require.True(t, completed.SnapshotCreated)
	require.Equal(t, "snapshot-abc", completed.SnapshotID)
	require.False(t, domain.BackupRunRestoreEligible(completed))
	require.Len(t, repo.verifications, 1)
	require.Len(t, publisher.eventsOfType(EventBackupVerificationChanged), 1)
	require.NotEmpty(t, completed.Error)
}

func TestBackupRegistryCompleteRunWithoutRequiredVerificationSkipsAndSucceeds(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	svc := NewBackupRegistryService(repo, nil, nil)
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, TargetRef: "fs:/srv/data"}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.recipes[recipe.ID] = recipe
	run := validBackupRun(recipe, backupRepo, nil, "event-1")
	repo.runs[run.ID] = run
	repo.coordinates[coordinate(run.RequestedBy, run.RequestKind, run.RequestDTag)] = run.ID

	completed, err := svc.CompleteBackupRun(ctx, run.ID, "snapshot-abc", nil, nil)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, completed.Status)
	require.Equal(t, domain.BackupVerificationSkipped, completed.VerificationStatus)
	require.False(t, domain.BackupRunRestoreEligible(completed))
}

func validBackupRun(recipe *domain.BackupRecipe, backupRepo *domain.BackupRepository, policy *domain.BackupPolicy, eventID string) *domain.BackupRun {
	run := &domain.BackupRun{
		ID:                 uuid.New(),
		RecipeID:           recipe.ID,
		RepositoryID:       backupRepo.ID,
		RequestedBy:        "pubkey-1",
		RequestEventID:     eventID,
		RequestKind:        38400,
		RequestDTag:        "daily:service-data",
		Status:             domain.RunStatusQueued,
		Backend:            domain.BackupBackendKopia,
		TargetRef:          recipe.TargetRef,
		VerificationStatus: domain.BackupVerificationPending,
	}
	if policy != nil {
		run.PolicyID = &policy.ID
	}
	return run
}

type recordingPublisher struct {
	events []events.Event
}

func (p *recordingPublisher) Publish(_ context.Context, e events.Event) {
	p.events = append(p.events, e)
}

func (p *recordingPublisher) Subscribe(events.EventType, events.Handler) {}

func (p *recordingPublisher) eventsOfType(typ events.EventType) []events.Event {
	out := []events.Event{}
	for _, e := range p.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

type fakeBackupRepo struct {
	recipes       map[uuid.UUID]*domain.BackupRecipe
	policies      map[uuid.UUID]*domain.BackupPolicy
	repositories  map[uuid.UUID]*domain.BackupRepository
	definitions   map[uuid.UUID]*domain.BackupDefinition
	runs          map[uuid.UUID]*domain.BackupRun
	coordinates   map[string]uuid.UUID
	verifications map[uuid.UUID]*domain.BackupVerificationRecord
	verifyByRun   map[uuid.UUID]uuid.UUID
}

func newFakeBackupRepo() *fakeBackupRepo {
	return &fakeBackupRepo{
		recipes:       map[uuid.UUID]*domain.BackupRecipe{},
		policies:      map[uuid.UUID]*domain.BackupPolicy{},
		repositories:  map[uuid.UUID]*domain.BackupRepository{},
		definitions:   map[uuid.UUID]*domain.BackupDefinition{},
		runs:          map[uuid.UUID]*domain.BackupRun{},
		coordinates:   map[string]uuid.UUID{},
		verifications: map[uuid.UUID]*domain.BackupVerificationRecord{},
		verifyByRun:   map[uuid.UUID]uuid.UUID{},
	}
}

func (r *fakeBackupRepo) UpsertBackupRecipe(_ context.Context, recipe *domain.BackupRecipe) error {
	cp := *recipe
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.recipes[cp.ID] = &cp
	return nil
}
func (r *fakeBackupRepo) GetBackupRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	return r.recipes[id], nil
}
func (r *fakeBackupRepo) GetBackupRecipeByNameVersion(_ context.Context, name, version string) (*domain.BackupRecipe, error) {
	for _, recipe := range r.recipes {
		if recipe.Name == name && recipe.Version == version {
			return recipe, nil
		}
	}
	return nil, nil
}
func (r *fakeBackupRepo) ListBackupRecipes(context.Context, int, int) ([]domain.BackupRecipe, error) {
	out := []domain.BackupRecipe{}
	for _, recipe := range r.recipes {
		out = append(out, *recipe)
	}
	return out, nil
}
func (r *fakeBackupRepo) UpsertBackupPolicy(_ context.Context, policy *domain.BackupPolicy) error {
	cp := *policy
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.policies[cp.ID] = &cp
	return nil
}
func (r *fakeBackupRepo) GetBackupPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	return r.policies[id], nil
}
func (r *fakeBackupRepo) GetBackupPolicyByName(_ context.Context, name string) (*domain.BackupPolicy, error) {
	for _, policy := range r.policies {
		if policy.Name == name {
			return policy, nil
		}
	}
	return nil, nil
}
func (r *fakeBackupRepo) ListBackupPolicies(context.Context, int, int) ([]domain.BackupPolicy, error) {
	out := []domain.BackupPolicy{}
	for _, policy := range r.policies {
		out = append(out, *policy)
	}
	return out, nil
}
func (r *fakeBackupRepo) UpsertBackupRepository(_ context.Context, repo *domain.BackupRepository) error {
	cp := *repo
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.repositories[cp.ID] = &cp
	return nil
}
func (r *fakeBackupRepo) GetBackupRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	return r.repositories[id], nil
}
func (r *fakeBackupRepo) GetBackupRepositoryByName(_ context.Context, name string) (*domain.BackupRepository, error) {
	for _, repo := range r.repositories {
		if repo.Name == name {
			return repo, nil
		}
	}
	return nil, nil
}
func (r *fakeBackupRepo) ListBackupRepositories(context.Context, int, int) ([]domain.BackupRepository, error) {
	out := []domain.BackupRepository{}
	for _, repo := range r.repositories {
		out = append(out, *repo)
	}
	return out, nil
}
func (r *fakeBackupRepo) UpsertBackupDefinition(_ context.Context, definition *domain.BackupDefinition) error {
	cp := *definition
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.definitions[cp.ID] = &cp
	return nil
}
func (r *fakeBackupRepo) GetBackupDefinition(_ context.Context, id uuid.UUID) (*domain.BackupDefinition, error) {
	return r.definitions[id], nil
}
func (r *fakeBackupRepo) GetBackupDefinitionByName(_ context.Context, name string) (*domain.BackupDefinition, error) {
	for _, definition := range r.definitions {
		if definition.Name == name {
			return definition, nil
		}
	}
	return nil, nil
}
func (r *fakeBackupRepo) ListBackupDefinitions(context.Context, int, int) ([]domain.BackupDefinition, error) {
	out := []domain.BackupDefinition{}
	for _, definition := range r.definitions {
		out = append(out, *definition)
	}
	return out, nil
}
func (r *fakeBackupRepo) DeleteBackupDefinition(_ context.Context, id uuid.UUID) error {
	delete(r.definitions, id)
	return nil
}
func (r *fakeBackupRepo) UpsertBackupRun(_ context.Context, run *domain.BackupRun) error {
	cp := *run
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.runs[cp.ID] = &cp
	r.coordinates[coordinate(cp.RequestedBy, cp.RequestKind, cp.RequestDTag)] = cp.ID
	return nil
}
func (r *fakeBackupRepo) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	return r.runs[id], nil
}
func (r *fakeBackupRepo) GetBackupRunByRequestCoordinate(_ context.Context, pubkey string, kind int, dTag string) (*domain.BackupRun, error) {
	id, ok := r.coordinates[coordinate(pubkey, kind, dTag)]
	if !ok {
		return nil, nil
	}
	return r.runs[id], nil
}
func (r *fakeBackupRepo) CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	if existing, _ := r.GetBackupRunByRequestCoordinate(ctx, run.RequestedBy, run.RequestKind, run.RequestDTag); existing != nil {
		return existing, false, nil
	}
	if err := r.UpsertBackupRun(ctx, run); err != nil {
		return nil, false, err
	}
	return r.runs[run.ID], true, nil
}
func (r *fakeBackupRepo) ClaimNextQueuedBackupRun(context.Context) (*domain.BackupRun, error) {
	for _, run := range r.runs {
		if run.Status == domain.RunStatusQueued {
			run.Status = domain.RunStatusRunning
			return run, nil
		}
	}
	return nil, nil
}
func (r *fakeBackupRepo) RequeueStaleBackupRuns(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (r *fakeBackupRepo) ListBackupRuns(_ context.Context, status domain.DeploymentRunStatus, _ int, _ int) ([]domain.BackupRun, error) {
	out := []domain.BackupRun{}
	for _, run := range r.runs {
		if status == "" || run.Status == status {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (r *fakeBackupRepo) UpsertBackupRestore(context.Context, *domain.BackupRestoreRun) error {
	return nil
}
func (r *fakeBackupRepo) GetBackupRestore(context.Context, uuid.UUID) (*domain.BackupRestoreRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) GetBackupRestoreByRequestCoordinate(context.Context, string, int, string) (*domain.BackupRestoreRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) CreateBackupRestoreIfAbsent(_ context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	return restore, true, nil
}
func (r *fakeBackupRepo) ClaimNextQueuedBackupRestore(context.Context) (*domain.BackupRestoreRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) RequeueStaleBackupRestores(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (r *fakeBackupRepo) ListBackupRestores(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRestoreRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) UpsertBackupRetentionRun(context.Context, *domain.BackupRetentionRun) error {
	return nil
}
func (r *fakeBackupRepo) GetBackupRetentionRun(context.Context, uuid.UUID) (*domain.BackupRetentionRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) GetBackupRetentionRunByRequestCoordinate(context.Context, string, int, string) (*domain.BackupRetentionRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) CreateBackupRetentionRunIfAbsent(_ context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	return run, true, nil
}
func (r *fakeBackupRepo) ClaimNextQueuedBackupRetentionRun(context.Context) (*domain.BackupRetentionRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) RequeueStaleBackupRetentionRuns(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (r *fakeBackupRepo) ListBackupRetentionRuns(context.Context, domain.DeploymentRunStatus, int, int) ([]domain.BackupRetentionRun, error) {
	return nil, nil
}
func (r *fakeBackupRepo) UpsertBackupVerification(_ context.Context, record *domain.BackupVerificationRecord) error {
	cp := *record
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if existingID, ok := r.verifyByRun[cp.BackupRunID]; ok {
		cp.ID = existingID
	}
	record.ID = cp.ID
	r.verifications[cp.ID] = &cp
	r.verifyByRun[cp.BackupRunID] = cp.ID
	return nil
}
func (r *fakeBackupRepo) GetBackupVerification(_ context.Context, id uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return r.verifications[id], nil
}
func (r *fakeBackupRepo) GetBackupVerificationByRunID(_ context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	id, ok := r.verifyByRun[runID]
	if !ok {
		return nil, nil
	}
	return r.verifications[id], nil
}

func coordinate(pubkey string, kind int, dTag string) string {
	return fmt.Sprintf("%s:%d:%s", pubkey, kind, dTag)
}
