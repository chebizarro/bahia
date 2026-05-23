package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestBackupSchedulerDispatchesLatestDueRunAndAccountsMissedRuns(t *testing.T) {
	ctx := context.Background()
	repo, definition := seededScheduledBackupDefinition(t, "@every 1h")
	now := time.Date(2026, 5, 23, 3, 10, 0, 0, time.UTC)
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil, WithBackupSchedulerClock(func() time.Time { return now }))

	result, err := scheduler.ProcessDueSchedules(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.Dispatched)
	require.Equal(t, 2, result.MissedRuns)
	require.Len(t, repo.runs, 1)
	dispatch := result.Dispatches[0]
	require.Equal(t, definition.ID, dispatch.DefinitionID)
	require.Equal(t, time.Date(2026, 5, 23, 3, 0, 0, 0, time.UTC), dispatch.ScheduledRunDueAt)
	require.True(t, dispatch.Created)
	state := repo.scheduleStates[definition.ID]
	require.NotNil(t, state)
	require.Equal(t, 2, state.MissedRunCount)
	require.Equal(t, now, *state.LastScheduledDispatch)
	require.Equal(t, time.Date(2026, 5, 23, 4, 0, 0, 0, time.UTC), *state.NextScheduledRun)
	for _, run := range repo.runs {
		require.Equal(t, BackupSchedulerInternalRequester, run.RequestedBy)
		require.Equal(t, BackupSchedulerRunRequestKind, run.RequestKind)
		require.Equal(t, definition.ID.String(), run.Metadata[backupRunMetadataScheduleDefinitionID])
		require.True(t, run.Metadata[backupRunMetadataScheduled].(bool))
	}
}

func TestBackupSchedulerIsIdempotentForSameDueCoordinate(t *testing.T) {
	ctx := context.Background()
	repo, _ := seededScheduledBackupDefinition(t, "@every 1h")
	now := time.Date(2026, 5, 23, 1, 5, 0, 0, time.UTC)
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil, WithBackupSchedulerClock(func() time.Time { return now }))

	first, err := scheduler.ProcessDueSchedules(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, first.Dispatched)

	// Simulate a retry before state persistence advanced the last-due marker.
	for _, state := range repo.scheduleStates {
		state.LastScheduledRunDueAt = nil
		state.LastScheduledDispatch = nil
		state.NextScheduledRun = nil
		state.MissedRunCount = 0
	}
	second, err := scheduler.ProcessDueSchedules(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, second.Dispatched)
	require.False(t, second.Dispatches[0].Created)
	require.Len(t, repo.runs, 1)
}

func TestBackupSchedulerHonorsPauseAndDisableLifecycle(t *testing.T) {
	ctx := context.Background()
	repo, definition := seededScheduledBackupDefinition(t, "@every 1h")
	now := time.Date(2026, 5, 23, 1, 5, 0, 0, time.UTC)
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil, WithBackupSchedulerClock(func() time.Time { return now }))

	require.NoError(t, scheduler.PauseSchedule(ctx, definition.ID, "storage maintenance", "operator-pubkey"))
	paused, err := scheduler.ProcessDueSchedules(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, paused.Skipped)
	require.Empty(t, repo.runs)
	require.Equal(t, "storage maintenance", repo.scheduleStates[definition.ID].PauseReason)

	require.NoError(t, scheduler.ResumeSchedule(ctx, definition.ID))
	require.Empty(t, repo.scheduleStates[definition.ID].PauseReason)
	resumed, err := scheduler.ProcessDueSchedules(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, resumed.Dispatched)

	require.NoError(t, scheduler.SetScheduleEnabled(ctx, definition.ID, false, "operator-pubkey", "definition retired"))
	require.False(t, repo.definitions[definition.ID].ScheduleEnabled)
	require.Equal(t, "definition retired", repo.scheduleStates[definition.ID].DisableReason)
}

func TestBackupSchedulerAppliesJitterAndMaintenanceWindowBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	repo, definition := seededScheduledBackupDefinition(t, "0 2 * * *")
	definition.ScheduleJitterWindow = "15m"
	definition.Metadata[domain.BackupDefinitionMetadataMaintenanceWindow] = "03:00-04:00"
	dueAt := time.Date(2026, 5, 23, 2, 0, 0, 0, time.UTC)
	repo.scheduleStates[definition.ID] = &domain.BackupScheduleState{DefinitionID: definition.ID, LastScheduledRunDueAt: ptrTime(time.Date(2026, 5, 22, 2, 0, 0, 0, time.UTC))}
	expectedEffective, err := effectiveScheduledTime(definition, dueAt)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 5, 23, 3, 0, 0, 0, time.UTC), expectedEffective)
	now := time.Date(2026, 5, 23, 3, 1, 0, 0, time.UTC)
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil, WithBackupSchedulerClock(func() time.Time { return now }))

	result, err := scheduler.ProcessDueSchedules(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, result.Dispatched)
	require.Equal(t, dueAt, result.Dispatches[0].ScheduledRunDueAt)
	require.Equal(t, expectedEffective, result.Dispatches[0].EffectiveDispatchTime)
	state := repo.scheduleStates[definition.ID]
	require.Equal(t, "03:00-04:00", state.MaintenanceWindow)
	require.NotNil(t, state.NextScheduledRun)
}

func TestBackupSchedulerSkipsInvalidScheduleExpressionWithoutBlockingPass(t *testing.T) {
	ctx := context.Background()
	repo, _ := seededScheduledBackupDefinition(t, "not-a-schedule")
	now := time.Date(2026, 5, 23, 1, 5, 0, 0, time.UTC)
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil, WithBackupSchedulerClock(func() time.Time { return now }))

	result, err := scheduler.ProcessDueSchedules(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Len(t, result.Errors, 1)
	require.Contains(t, result.Errors[0].Message, domain.ErrInvalidValue.Error())
	require.Empty(t, repo.runs)
}

func TestBackupSchedulerRejectsSubSecondCadence(t *testing.T) {
	_, err := parseBackupSchedule("@every 500ms")

	require.ErrorIs(t, err, domain.ErrInvalidValue)
}

func TestBackupSchedulerPauseRequiresExistingDefinition(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBackupRepo()
	scheduler := NewBackupSchedulerService(NewBackupRegistryService(repo, nil, nil), nil)

	err := scheduler.PauseSchedule(ctx, uuid.New(), "maintenance", "operator")

	require.ErrorIs(t, err, repository.ErrNotFound)
	require.Empty(t, repo.scheduleStates)
}

func TestBackupSchedulerComputesNextCronRunWithoutSleeping(t *testing.T) {
	plan, err := parseBackupSchedule("*/15 1-2 * * *")
	require.NoError(t, err)

	next, err := plan.Next(time.Date(2026, 5, 23, 1, 44, 30, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 5, 23, 1, 45, 0, 0, time.UTC), next)
}

func seededScheduledBackupDefinition(t *testing.T, expression string) (*fakeBackupRepo, *domain.BackupDefinition) {
	t.Helper()
	repo := newFakeBackupRepo()
	backupRepo := &domain.BackupRepository{ID: uuid.New(), Name: "prod-kopia", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://prod"}
	policy := &domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	recipe := &domain.BackupRecipe{ID: uuid.New(), Name: "service-data", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: backupRepo.ID, PolicyID: &policy.ID, TargetRef: "fs:/srv/data", VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	definition := &domain.BackupDefinition{
		ID:                   uuid.New(),
		Name:                 "daily-service",
		RepositoryID:         backupRepo.ID,
		RepositoryName:       backupRepo.Name,
		PolicyID:             policy.ID,
		PolicyName:           policy.Name,
		RecipeID:             recipe.ID,
		RecipeName:           recipe.Name,
		RecipeVersion:        recipe.Version,
		ScheduleExpression:   expression,
		ScheduleEnabled:      true,
		ScheduleJitterWindow: "",
		Metadata:             map[string]any{},
		CreatedAt:            time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
		CreatedBy:            "creator-pubkey",
	}
	repo.repositories[backupRepo.ID] = backupRepo
	repo.policies[policy.ID] = policy
	repo.recipes[recipe.ID] = recipe
	repo.definitions[definition.ID] = definition
	return repo, definition
}

func ptrTime(t time.Time) *time.Time { return &t }
