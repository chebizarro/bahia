package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgManagedInstanceHealthRepository is the PostgreSQL managed-instance health repository.
type PgManagedInstanceHealthRepository struct {
	pool pgQueryer
}

func NewPgManagedInstanceHealthRepository(pool *pgxpool.Pool) *PgManagedInstanceHealthRepository {
	return newPgManagedInstanceHealthRepositoryWithDB(pool)
}

func newPgManagedInstanceHealthRepositoryWithDB(db pgQueryer) *PgManagedInstanceHealthRepository {
	return &PgManagedInstanceHealthRepository{pool: db}
}

const managedHealthColumns = `service_id, environment_id, deployment_unit_id, runtime_target_name, host, supervisor_type, status, failure_reason, last_observed_at, restart_count, consecutive_restart_count, memory_current_bytes, memory_peak_bytes, memory_limit_bytes, last_recovery_attempt, updated_at`
const managedHealthEventColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, previous_status, status, reason, evidence, observed_at`
const recoveryAttemptColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, correlation_id, requested_at, result, evidence`
const maintenanceOverrideColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, actor, reason, created_at, expires_at`

func (r *PgManagedInstanceHealthRepository) UpsertHealth(ctx context.Context, health *domain.ManagedInstanceHealth) error {
	if health == nil {
		return fmt.Errorf("managed instance health is required")
	}
	if health.LastObservedAt.IsZero() {
		health.LastObservedAt = time.Now().UTC()
	}
	health.UpdatedAt = time.Now().UTC()
	health.FailureReason = domain.SanitizeEvidence(health.FailureReason)

	var lastAttempt any
	if health.LastRecoveryAttempt != nil {
		attempt := *health.LastRecoveryAttempt
		attempt.Evidence = domain.SanitizeEvidence(attempt.Evidence)
		encoded, err := marshalJSON(&attempt, "last recovery attempt")
		if err != nil {
			return err
		}
		lastAttempt = encoded
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO managed_instance_health (`+managedHealthColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (service_id, environment_id, deployment_unit_id, runtime_target_name) DO UPDATE SET
			host = EXCLUDED.host,
			supervisor_type = EXCLUDED.supervisor_type,
			status = EXCLUDED.status,
			failure_reason = EXCLUDED.failure_reason,
			last_observed_at = EXCLUDED.last_observed_at,
			restart_count = EXCLUDED.restart_count,
			consecutive_restart_count = EXCLUDED.consecutive_restart_count,
			memory_current_bytes = EXCLUDED.memory_current_bytes,
			memory_peak_bytes = EXCLUDED.memory_peak_bytes,
			memory_limit_bytes = EXCLUDED.memory_limit_bytes,
			last_recovery_attempt = EXCLUDED.last_recovery_attempt,
			updated_at = EXCLUDED.updated_at
		WHERE EXCLUDED.last_observed_at >= managed_instance_health.last_observed_at
	`, health.ServiceID, health.EnvironmentID, health.DeploymentUnitID, health.RuntimeTargetName,
		health.Host, health.SupervisorType, health.Status, health.FailureReason, health.LastObservedAt,
		health.RestartCount, health.ConsecutiveRestartCount, health.MemoryCurrentBytes, health.MemoryPeakBytes,
		health.MemoryLimitBytes, lastAttempt, health.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting managed instance health: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanManagedHealth(row rowScanner) (*domain.ManagedInstanceHealth, error) {
	health := &domain.ManagedInstanceHealth{}
	var lastAttemptJSON []byte
	if err := row.Scan(&health.ServiceID, &health.EnvironmentID, &health.DeploymentUnitID, &health.RuntimeTargetName,
		&health.Host, &health.SupervisorType, &health.Status, &health.FailureReason, &health.LastObservedAt,
		&health.RestartCount, &health.ConsecutiveRestartCount, &health.MemoryCurrentBytes, &health.MemoryPeakBytes,
		&health.MemoryLimitBytes, &lastAttemptJSON, &health.UpdatedAt); err != nil {
		return nil, err
	}
	if len(lastAttemptJSON) > 0 && string(lastAttemptJSON) != "null" {
		health.LastRecoveryAttempt = &domain.RecoveryAttempt{}
		if err := unmarshalJSON(lastAttemptJSON, health.LastRecoveryAttempt, "last recovery attempt"); err != nil {
			return nil, err
		}
	}
	return health, nil
}

func (r *PgManagedInstanceHealthRepository) GetHealth(ctx context.Context, key domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+managedHealthColumns+` FROM managed_instance_health
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id = $3 AND runtime_target_name = $4`,
		key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName)
	health, err := scanManagedHealth(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying managed instance health: %w", err)
	}
	return health, nil
}

func (r *PgManagedInstanceHealthRepository) listHealth(ctx context.Context, query string, args ...any) ([]domain.ManagedInstanceHealth, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.ManagedInstanceHealth
	for rows.Next() {
		health, err := scanManagedHealth(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning managed instance health: %w", err)
		}
		result = append(result, *health)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating managed instance health: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) ListHealthByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	result, err := r.listHealth(ctx, `SELECT `+managedHealthColumns+` FROM managed_instance_health WHERE environment_id = $1 ORDER BY last_observed_at DESC`, environmentID)
	if err != nil {
		return nil, fmt.Errorf("listing managed instance health by environment: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) ListHealthByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	result, err := r.listHealth(ctx, `SELECT `+managedHealthColumns+` FROM managed_instance_health WHERE service_id = $1 ORDER BY last_observed_at DESC`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing managed instance health by service: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) ListUnhealthy(ctx context.Context) ([]domain.ManagedInstanceHealth, error) {
	result, err := r.listHealth(ctx, `SELECT `+managedHealthColumns+` FROM managed_instance_health WHERE status NOT IN ('healthy', 'running') ORDER BY last_observed_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing unhealthy managed instances: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) AppendHealthEvent(ctx context.Context, event *domain.ManagedInstanceHealthEvent) error {
	if event == nil {
		return fmt.Errorf("managed instance health event is required")
	}
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	event.Reason = domain.SanitizeEvidence(event.Reason)
	event.Evidence = domain.SanitizeEvidence(event.Evidence)
	_, err := r.pool.Exec(ctx, `INSERT INTO managed_instance_health_events (`+managedHealthEventColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, event.ID, event.ServiceID,
		event.EnvironmentID, event.DeploymentUnitID, event.RuntimeTargetName, event.PreviousStatus, event.Status,
		event.Reason, event.Evidence, event.ObservedAt)
	if err != nil {
		return fmt.Errorf("appending managed instance health event: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) ListRecentHealthEvents(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.ManagedInstanceHealthEvent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+managedHealthEventColumns+` FROM managed_instance_health_events
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id = $3 AND runtime_target_name = $4
		ORDER BY observed_at DESC LIMIT $5`, key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName, boundedHistoryLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("listing recent managed instance health events: %w", err)
	}
	defer rows.Close()
	var result []domain.ManagedInstanceHealthEvent
	for rows.Next() {
		var event domain.ManagedInstanceHealthEvent
		if err := rows.Scan(&event.ID, &event.ServiceID, &event.EnvironmentID, &event.DeploymentUnitID,
			&event.RuntimeTargetName, &event.PreviousStatus, &event.Status, &event.Reason, &event.Evidence,
			&event.ObservedAt); err != nil {
			return nil, fmt.Errorf("scanning managed instance health event: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating managed instance health events: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) RecordRecoveryAttempt(ctx context.Context, attempt *domain.RecoveryAttempt) (bool, error) {
	if attempt == nil {
		return false, fmt.Errorf("recovery attempt is required")
	}
	if attempt.ID == uuid.Nil {
		attempt.ID = uuid.New()
	}
	if attempt.RequestedAt.IsZero() {
		attempt.RequestedAt = time.Now().UTC()
	}
	attempt.Evidence = domain.SanitizeEvidence(attempt.Evidence)
	command, err := r.pool.Exec(ctx, `INSERT INTO managed_instance_recovery_attempts (`+recoveryAttemptColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (correlation_id) DO NOTHING`, attempt.ID, attempt.ServiceID, attempt.EnvironmentID,
		attempt.DeploymentUnitID, attempt.RuntimeTargetName, attempt.CorrelationID, attempt.RequestedAt,
		attempt.Result, attempt.Evidence)
	if err != nil {
		return false, fmt.Errorf("recording managed instance recovery attempt: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (r *PgManagedInstanceHealthRepository) ListRecentRecoveryAttempts(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+recoveryAttemptColumns+` FROM managed_instance_recovery_attempts
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id = $3 AND runtime_target_name = $4
		ORDER BY requested_at DESC LIMIT $5`, key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName, boundedHistoryLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("listing recent managed instance recovery attempts: %w", err)
	}
	defer rows.Close()
	var result []domain.RecoveryAttempt
	for rows.Next() {
		var attempt domain.RecoveryAttempt
		if err := rows.Scan(&attempt.ID, &attempt.ServiceID, &attempt.EnvironmentID, &attempt.DeploymentUnitID,
			&attempt.RuntimeTargetName, &attempt.CorrelationID, &attempt.RequestedAt, &attempt.Result,
			&attempt.Evidence); err != nil {
			return nil, fmt.Errorf("scanning managed instance recovery attempt: %w", err)
		}
		result = append(result, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating managed instance recovery attempts: %w", err)
	}
	return result, nil
}

func (r *PgManagedInstanceHealthRepository) CreateMaintenanceOverride(ctx context.Context, override *domain.MaintenanceOverride) error {
	if override == nil {
		return fmt.Errorf("maintenance override is required")
	}
	if override.ID == uuid.Nil {
		override.ID = uuid.New()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	override.Reason = domain.SanitizeEvidence(override.Reason)
	_, err := r.pool.Exec(ctx, `INSERT INTO managed_instance_overrides (`+maintenanceOverrideColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (service_id, environment_id, deployment_unit_id, runtime_target_name) DO UPDATE SET
			id = EXCLUDED.id, actor = EXCLUDED.actor, reason = EXCLUDED.reason,
			created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at`, override.ID, override.ServiceID,
		override.EnvironmentID, override.DeploymentUnitID, override.RuntimeTargetName, override.Actor,
		override.Reason, override.CreatedAt, override.ExpiresAt)
	if err != nil {
		return fmt.Errorf("creating managed instance maintenance override: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) ClearMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM managed_instance_overrides
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id = $3 AND runtime_target_name = $4`,
		key.ServiceID, key.EnvironmentID, key.DeploymentUnitID, key.RuntimeTargetName)
	if err != nil {
		return fmt.Errorf("clearing managed instance maintenance override: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) GetActiveMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+maintenanceOverrideColumns+` FROM managed_instance_overrides
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id = $3 AND runtime_target_name = $4
		AND created_at <= $5 AND (expires_at IS NULL OR expires_at > $5)`, key.ServiceID, key.EnvironmentID,
		key.DeploymentUnitID, key.RuntimeTargetName, at)
	var override domain.MaintenanceOverride
	if err := row.Scan(&override.ID, &override.ServiceID, &override.EnvironmentID, &override.DeploymentUnitID,
		&override.RuntimeTargetName, &override.Actor, &override.Reason, &override.CreatedAt, &override.ExpiresAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying active managed instance maintenance override: %w", err)
	}
	return &override, nil
}

func boundedHistoryLimit(limit int) int {
	const (
		defaultLimit = 50
		maxLimit     = 500
	)
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
