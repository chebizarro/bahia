package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgManagedInstanceHealthRepositoryUpsertSanitizesStoredSummary(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	health := &domain.ManagedInstanceHealth{
		ManagedInstanceKey: domain.ManagedInstanceKey{
			ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api",
		},
		Host:           "https://host-user:host-password@example.test",
		SupervisorType: domain.InstanceSupervisorDocker,
		Status:         domain.InstanceHealthStatusUnhealthy,
		FailureReason:  "token=do-not-store",
		LastRecoveryAttempt: &domain.RecoveryAttempt{
			CorrelationID: "attempt-1", Evidence: "bunker://secret?relay=wss://relay.example",
		},
	}
	args := managedInstanceAnyArgs(17)
	args[4] = "https://[REDACTED]@example.test"
	args[7] = "[REDACTED]"
	mock.ExpectExec("INSERT INTO managed_instance_health").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, repo.UpsertHealth(context.Background(), health))
	require.Equal(t, "[REDACTED]", health.FailureReason)
	require.Equal(t, "https://[REDACTED]@example.test", health.Host)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryPersistsImplicitUnitObservation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	health := &domain.ManagedInstanceHealth{
		ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), RuntimeTargetName: "api"},
		SupervisorType:     domain.InstanceSupervisorDocker,
		Status:             domain.InstanceHealthStatusHealthy,
	}
	args := managedInstanceAnyArgs(17)
	args[2] = nil
	mock.ExpectExec("INSERT INTO managed_instance_health").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, repo.UpsertHealth(context.Background(), health))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryGetHealthScansRecoverySummary(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	attemptID := uuid.New()
	attemptJSON, err := json.Marshal(domain.RecoveryAttempt{ID: attemptID, CorrelationID: "attempt-1", RequestedAt: now, Result: domain.RecoveryAttemptSuccess})
	require.NoError(t, err)
	rows := pgxmock.NewRows([]string{
		"service_id", "environment_id", "deployment_unit_id", "runtime_target_name", "host", "supervisor_type", "status", "failure_reason",
		"last_observed_at", "failure_generation_at", "restart_count", "consecutive_restart_count", "memory_current_bytes", "memory_peak_bytes", "memory_limit_bytes", "last_recovery_attempt", "updated_at",
	}).AddRow(key.ServiceID, key.EnvironmentID, key.DeploymentUnitID.String(), key.RuntimeTargetName, "host-a", domain.InstanceSupervisorDocker,
		domain.InstanceHealthStatusHealthy, "", now, nil, 2, 0, int64(10), int64(20), int64(100), attemptJSON, now)
	mock.ExpectQuery("SELECT .* FROM managed_instance_health").WithArgs(key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName).WillReturnRows(rows)

	got, err := repo.GetHealth(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.LastRecoveryAttempt)
	require.Equal(t, "attempt-1", got.LastRecoveryAttempt.CorrelationID)
	require.Equal(t, domain.RecoveryAttemptSuccess, got.LastRecoveryAttempt.Result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryRecordAttemptIsIdempotentAndSanitized(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	attempt := &domain.RecoveryAttempt{
		ManagedInstanceKey: domain.ManagedInstanceKey{
			ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api",
		},
		CorrelationID: "stable-correlation",
		RequestedAt:   time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		Result:        domain.RecoveryAttemptFailed,
		Evidence:      "api_key=do-not-store",
	}
	args := managedInstanceAnyArgs(9)
	args[5] = attempt.CorrelationID
	args[8] = "[REDACTED]"
	mock.ExpectExec("ON CONFLICT \\(correlation_id\\) DO NOTHING").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	inserted, err := repo.RecordRecoveryAttempt(context.Background(), attempt)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, "[REDACTED]", attempt.Evidence)

	args = managedInstanceAnyArgs(9)
	args[5] = attempt.CorrelationID
	args[8] = "[REDACTED]"
	mock.ExpectExec("ON CONFLICT \\(correlation_id\\) DO NOTHING").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	inserted, err = repo.RecordRecoveryAttempt(context.Background(), attempt)
	require.NoError(t, err)
	require.False(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryCompletePendingAttempt(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	mock.ExpectExec("UPDATE managed_instance_recovery_attempts").WithArgs("corr", domain.RecoveryAttemptSuccess, "[REDACTED]", domain.RecoveryAttemptPending).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	completed, err := repo.CompleteRecoveryAttempt(context.Background(), "corr", domain.RecoveryAttemptSuccess, "token=secret")
	require.NoError(t, err)
	require.True(t, completed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryUpsertHealthWithEventCommitsTogether(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	health := &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: now}
	event := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, Status: health.Status, ObservedAt: now}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO managed_instance_health").WithArgs(managedInstanceAnyArgs(17)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO managed_instance_health_events").WithArgs(managedInstanceAnyArgs(10)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, repo.UpsertHealthWithEvent(context.Background(), health, event))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryUpsertHealthWithEventRollsBackTogether(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	health := &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: now}
	event := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, Status: health.Status, ObservedAt: now}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO managed_instance_health").WithArgs(managedInstanceAnyArgs(17)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO managed_instance_health_events").WithArgs(managedInstanceAnyArgs(10)...).WillReturnError(errors.New("event write failed"))
	mock.ExpectRollback()

	require.ErrorContains(t, repo.UpsertHealthWithEvent(context.Background(), health, event), "event write failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryCompleteRecoveryWithHealthEventCommitsTogether(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	health := &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusHealthy, LastObservedAt: now}
	event := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, PreviousStatus: domain.InstanceHealthStatusUnhealthy, Status: health.Status, ObservedAt: now}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE managed_instance_recovery_attempts").WithArgs("corr", domain.RecoveryAttemptSuccess, "recovered", domain.RecoveryAttemptPending).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("INSERT INTO managed_instance_health").WithArgs(managedInstanceAnyArgs(17)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO managed_instance_health_events").WithArgs(managedInstanceAnyArgs(10)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	completed, err := repo.CompleteRecoveryAttemptWithHealthEvent(context.Background(), "corr", domain.RecoveryAttemptSuccess, "recovered", health, event)
	require.NoError(t, err)
	require.True(t, completed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryRecentReadsAreBounded(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	mock.ExpectQuery("FROM managed_instance_health_events").
		WithArgs(key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName, 500).
		WillReturnRows(pgxmock.NewRows([]string{"id", "service_id", "environment_id", "deployment_unit_id", "runtime_target_name", "previous_status", "status", "reason", "evidence", "observed_at"}))

	events, err := repo.ListRecentHealthEvents(context.Background(), key, 5000)
	require.NoError(t, err)
	require.Empty(t, events)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositorySanitizesMaintenanceActorAndReason(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	override := &domain.MaintenanceOverride{
		ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), RuntimeTargetName: "api"},
		Actor:              "https://actor:password@example.test",
		Reason:             "eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZA.c2lnbmF0dXJl",
	}
	args := managedInstanceAnyArgs(9)
	args[3] = nil
	args[5] = "https://[REDACTED]@example.test"
	args[6] = "[REDACTED]"
	mock.ExpectExec("INSERT INTO managed_instance_overrides").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, repo.CreateMaintenanceOverride(context.Background(), override))
	require.Equal(t, "https://[REDACTED]@example.test", override.Actor)
	require.Equal(t, "[REDACTED]", override.Reason)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgManagedInstanceHealthRepositoryGetActiveOverrideReturnsNilWhenAbsent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgManagedInstanceHealthRepositoryWithDB(mock)
	key := domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM managed_instance_overrides").
		WithArgs(key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName, now).
		WillReturnRows(pgxmock.NewRows([]string{"id", "service_id", "environment_id", "deployment_unit_id", "runtime_target_name", "actor", "reason", "created_at", "expires_at"}))

	override, err := repo.GetActiveMaintenanceOverride(context.Background(), key, now)
	require.NoError(t, err)
	require.Nil(t, override)
	require.NoError(t, mock.ExpectationsWereMet())
}

func managedInstanceAnyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
