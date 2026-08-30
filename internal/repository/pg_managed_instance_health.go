package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

const managedHealthColumns = `service_id, environment_id, deployment_unit_id, runtime_target_name, host, supervisor_type, status, failure_reason, last_observed_at, failure_generation_at, restart_count, consecutive_restart_count, memory_current_bytes, memory_peak_bytes, memory_limit_bytes, last_recovery_attempt, updated_at`
const managedHealthListColumns = `h.service_id, h.environment_id, h.deployment_unit_id, h.runtime_target_name, h.host, h.supervisor_type, h.status, h.failure_reason, h.last_observed_at, h.failure_generation_at, h.restart_count, h.consecutive_restart_count, h.memory_current_bytes, h.memory_peak_bytes, h.memory_limit_bytes, h.last_recovery_attempt, h.updated_at`
const managedHealthEventColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, previous_status, status, reason, evidence, observed_at`
const recoveryAttemptColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, correlation_id, requested_at, result, evidence`
const maintenanceOverrideColumns = `id, service_id, environment_id, deployment_unit_id, runtime_target_name, actor, reason, created_at, expires_at`

// UpsertHealthWithEvent atomically persists the current health snapshot and its durable transition.
func (r *PgManagedInstanceHealthRepository) UpsertHealthWithEvent(ctx context.Context, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) error {
	if event == nil {
		return fmt.Errorf("managed instance health event is required")
	}
	return r.withinTx(ctx, func(txRepo *PgManagedInstanceHealthRepository) error {
		if err := txRepo.UpsertHealth(ctx, health); err != nil {
			return err
		}
		return txRepo.AppendHealthEvent(ctx, event)
	})
}

func (r *PgManagedInstanceHealthRepository) UpsertHealth(ctx context.Context, health *domain.ManagedInstanceHealth) error {
	if health == nil {
		return fmt.Errorf("managed instance health is required")
	}
	if health.LastObservedAt.IsZero() {
		health.LastObservedAt = time.Now().UTC()
	}
	health.UpdatedAt = time.Now().UTC()
	health.Host = domain.SanitizeEvidence(health.Host)
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
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (service_id, environment_id, deployment_unit_id, runtime_target_name) DO UPDATE SET
			host = EXCLUDED.host,
			supervisor_type = EXCLUDED.supervisor_type,
			status = EXCLUDED.status,
			failure_reason = EXCLUDED.failure_reason,
			last_observed_at = EXCLUDED.last_observed_at,
			failure_generation_at = EXCLUDED.failure_generation_at,
			restart_count = EXCLUDED.restart_count,
			consecutive_restart_count = EXCLUDED.consecutive_restart_count,
			memory_current_bytes = EXCLUDED.memory_current_bytes,
			memory_peak_bytes = EXCLUDED.memory_peak_bytes,
			memory_limit_bytes = EXCLUDED.memory_limit_bytes,
			last_recovery_attempt = EXCLUDED.last_recovery_attempt,
			updated_at = EXCLUDED.updated_at
		WHERE EXCLUDED.last_observed_at >= managed_instance_health.last_observed_at
	`, health.ServiceID, health.EnvironmentID, nullableDeploymentUnitID(health.DeploymentUnitID), health.RuntimeTargetName,
		health.Host, health.SupervisorType, health.Status, health.FailureReason, health.LastObservedAt, nullableTime(health.FailureGenerationAt),
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
	var deploymentUnitID pgtype.UUID
	var failureGenerationAt pgtype.Timestamptz
	if err := row.Scan(&health.ServiceID, &health.EnvironmentID, &deploymentUnitID, &health.RuntimeTargetName,
		&health.Host, &health.SupervisorType, &health.Status, &health.FailureReason, &health.LastObservedAt, &failureGenerationAt,
		&health.RestartCount, &health.ConsecutiveRestartCount, &health.MemoryCurrentBytes, &health.MemoryPeakBytes,
		&health.MemoryLimitBytes, &lastAttemptJSON, &health.UpdatedAt); err != nil {
		return nil, err
	}
	if deploymentUnitID.Valid {
		health.DeploymentUnitID = uuid.UUID(deploymentUnitID.Bytes)
	}
	if failureGenerationAt.Valid {
		health.FailureGenerationAt = failureGenerationAt.Time
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
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id IS NOT DISTINCT FROM $3 AND runtime_target_name = $4`,
		key.ServiceID, key.EnvironmentID, nullableDeploymentUnitID(key.DeploymentUnitID), key.RuntimeTargetName)
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

func (r *PgManagedInstanceHealthRepository) ListHealth(ctx context.Context, options ManagedInstanceHealthListOptions) ([]ManagedInstanceHealthListItem, error) {
	activeAt := options.ActiveAt
	if activeAt.IsZero() {
		activeAt = time.Now().UTC()
	}
	rows, err := r.pool.Query(ctx, `SELECT `+managedHealthListColumns+`,
		o.id, o.actor, o.reason, o.created_at, o.expires_at
		FROM managed_instance_health h
		JOIN services s ON s.id = h.service_id
		JOIN environments e ON e.id = h.environment_id
		LEFT JOIN managed_instance_overrides o
			ON o.service_id = h.service_id
			AND o.environment_id = h.environment_id
			AND o.deployment_unit_id IS NOT DISTINCT FROM h.deployment_unit_id
			AND o.runtime_target_name = h.runtime_target_name
			AND o.created_at <= $5
			AND (o.expires_at IS NULL OR o.expires_at > $5)
		WHERE ($1::uuid IS NULL OR (s.org_id = $1 AND e.org_id = $1))
			AND ($2::uuid IS NULL OR h.service_id = $2)
			AND ($3::uuid IS NULL OR h.environment_id = $3)
			AND (NOT $4 OR h.status NOT IN ('healthy', 'running'))
		ORDER BY h.last_observed_at DESC, h.service_id, h.environment_id, h.deployment_unit_id, h.runtime_target_name
		LIMIT $6 OFFSET $7`, nullableUUID(options.OrgID), nullableUUID(options.ServiceID), nullableUUID(options.EnvironmentID),
		options.UnhealthyOnly, activeAt, boundedHealthListLimit(options.Limit), nonNegativeOffset(options.Offset))
	if err != nil {
		return nil, fmt.Errorf("listing scoped managed instance health: %w", err)
	}
	defer rows.Close()

	result := make([]ManagedInstanceHealthListItem, 0)
	for rows.Next() {
		item, err := scanManagedHealthListItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning scoped managed instance health: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating scoped managed instance health: %w", err)
	}
	return result, nil
}

func scanManagedHealthListItem(row rowScanner) (ManagedInstanceHealthListItem, error) {
	var item ManagedInstanceHealthListItem
	var deploymentUnitID pgtype.UUID
	var failureGenerationAt pgtype.Timestamptz
	var lastAttemptJSON []byte
	var overrideID pgtype.UUID
	var overrideActor, overrideReason pgtype.Text
	var overrideCreatedAt, overrideExpiresAt pgtype.Timestamptz
	if err := row.Scan(&item.Health.ServiceID, &item.Health.EnvironmentID, &deploymentUnitID, &item.Health.RuntimeTargetName,
		&item.Health.Host, &item.Health.SupervisorType, &item.Health.Status, &item.Health.FailureReason, &item.Health.LastObservedAt, &failureGenerationAt,
		&item.Health.RestartCount, &item.Health.ConsecutiveRestartCount, &item.Health.MemoryCurrentBytes, &item.Health.MemoryPeakBytes,
		&item.Health.MemoryLimitBytes, &lastAttemptJSON, &item.Health.UpdatedAt, &overrideID, &overrideActor, &overrideReason,
		&overrideCreatedAt, &overrideExpiresAt); err != nil {
		return ManagedInstanceHealthListItem{}, err
	}
	if deploymentUnitID.Valid {
		item.Health.DeploymentUnitID = uuid.UUID(deploymentUnitID.Bytes)
	}
	if failureGenerationAt.Valid {
		item.Health.FailureGenerationAt = failureGenerationAt.Time
	}
	if len(lastAttemptJSON) > 0 && string(lastAttemptJSON) != "null" {
		item.Health.LastRecoveryAttempt = &domain.RecoveryAttempt{}
		if err := unmarshalJSON(lastAttemptJSON, item.Health.LastRecoveryAttempt, "last recovery attempt"); err != nil {
			return ManagedInstanceHealthListItem{}, err
		}
	}
	if overrideID.Valid {
		override := &domain.MaintenanceOverride{
			ID:                 uuid.UUID(overrideID.Bytes),
			ManagedInstanceKey: item.Health.ManagedInstanceKey,
			Actor:              overrideActor.String,
			Reason:             overrideReason.String,
			CreatedAt:          overrideCreatedAt.Time,
		}
		if overrideExpiresAt.Valid {
			expiresAt := overrideExpiresAt.Time
			override.ExpiresAt = &expiresAt
		}
		item.MaintenanceOverride = override
	}
	return item, nil
}

func (r *PgManagedInstanceHealthRepository) ListAllHealth(ctx context.Context) ([]domain.ManagedInstanceHealth, error) {
	result, err := r.listHealth(ctx, `SELECT `+managedHealthColumns+` FROM managed_instance_health ORDER BY last_observed_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing managed instance health: %w", err)
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
		event.EnvironmentID, nullableDeploymentUnitID(event.DeploymentUnitID), event.RuntimeTargetName, event.PreviousStatus, event.Status,
		event.Reason, event.Evidence, event.ObservedAt)
	if err != nil {
		return fmt.Errorf("appending managed instance health event: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) ListRecentHealthEvents(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.ManagedInstanceHealthEvent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+managedHealthEventColumns+` FROM managed_instance_health_events
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id IS NOT DISTINCT FROM $3 AND runtime_target_name = $4
		ORDER BY observed_at DESC LIMIT $5`, key.ServiceID, key.EnvironmentID, nullableDeploymentUnitID(key.DeploymentUnitID), key.RuntimeTargetName, boundedHistoryLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("listing recent managed instance health events: %w", err)
	}
	defer rows.Close()
	var result []domain.ManagedInstanceHealthEvent
	for rows.Next() {
		var event domain.ManagedInstanceHealthEvent
		var deploymentUnitID pgtype.UUID
		if err := rows.Scan(&event.ID, &event.ServiceID, &event.EnvironmentID, &deploymentUnitID,
			&event.RuntimeTargetName, &event.PreviousStatus, &event.Status, &event.Reason, &event.Evidence,
			&event.ObservedAt); err != nil {
			return nil, fmt.Errorf("scanning managed instance health event: %w", err)
		}
		if deploymentUnitID.Valid {
			event.DeploymentUnitID = uuid.UUID(deploymentUnitID.Bytes)
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
		nullableDeploymentUnitID(attempt.DeploymentUnitID), attempt.RuntimeTargetName, attempt.CorrelationID, attempt.RequestedAt,
		attempt.Result, attempt.Evidence)
	if err != nil {
		return false, fmt.Errorf("recording managed instance recovery attempt: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

// CompleteRecoveryAttempt atomically replaces a pending claim with its terminal result.
func (r *PgManagedInstanceHealthRepository) CompleteRecoveryAttempt(ctx context.Context, correlationID string, result domain.RecoveryAttemptResult, evidence string) (bool, error) {
	switch result {
	case domain.RecoveryAttemptSuccess, domain.RecoveryAttemptDegraded, domain.RecoveryAttemptFailed:
	default:
		return false, fmt.Errorf("invalid terminal recovery result %q", result)
	}
	command, err := r.pool.Exec(ctx, `UPDATE managed_instance_recovery_attempts SET result = $2, evidence = $3 WHERE correlation_id = $1 AND result = $4`, strings.TrimSpace(correlationID), result, domain.SanitizeEvidence(evidence), domain.RecoveryAttemptPending)
	if err != nil {
		return false, fmt.Errorf("completing managed instance recovery attempt: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

// CompleteRecoveryAttemptWithHealthEvent atomically completes a pending recovery and persists its resulting health snapshot and optional transition.
func (r *PgManagedInstanceHealthRepository) CompleteRecoveryAttemptWithHealthEvent(ctx context.Context, correlationID string, result domain.RecoveryAttemptResult, evidence string, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) (bool, error) {
	if health == nil {
		return false, fmt.Errorf("managed instance health is required")
	}
	completed := false
	err := r.withinTx(ctx, func(txRepo *PgManagedInstanceHealthRepository) error {
		var err error
		completed, err = txRepo.CompleteRecoveryAttempt(ctx, correlationID, result, evidence)
		if err != nil || !completed {
			return err
		}
		if err := txRepo.UpsertHealth(ctx, health); err != nil {
			return err
		}
		if event != nil {
			return txRepo.AppendHealthEvent(ctx, event)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return completed, nil
}

func (r *PgManagedInstanceHealthRepository) withinTx(ctx context.Context, fn func(*PgManagedInstanceHealthRepository) error) error {
	beginner, ok := r.pool.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return fmt.Errorf("managed instance health repository does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin managed instance health transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(newPgManagedInstanceHealthRepositoryWithDB(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit managed instance health transaction: %w", err)
	}
	committed = true
	return nil
}

func (r *PgManagedInstanceHealthRepository) ListRecentRecoveryAttempts(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+recoveryAttemptColumns+` FROM managed_instance_recovery_attempts
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id IS NOT DISTINCT FROM $3 AND runtime_target_name = $4
		ORDER BY requested_at DESC LIMIT $5`, key.ServiceID, key.EnvironmentID, nullableDeploymentUnitID(key.DeploymentUnitID), key.RuntimeTargetName, boundedHistoryLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("listing recent managed instance recovery attempts: %w", err)
	}
	defer rows.Close()
	var result []domain.RecoveryAttempt
	for rows.Next() {
		var attempt domain.RecoveryAttempt
		var deploymentUnitID pgtype.UUID
		if err := rows.Scan(&attempt.ID, &attempt.ServiceID, &attempt.EnvironmentID, &deploymentUnitID,
			&attempt.RuntimeTargetName, &attempt.CorrelationID, &attempt.RequestedAt, &attempt.Result,
			&attempt.Evidence); err != nil {
			return nil, fmt.Errorf("scanning managed instance recovery attempt: %w", err)
		}
		if deploymentUnitID.Valid {
			attempt.DeploymentUnitID = uuid.UUID(deploymentUnitID.Bytes)
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
	override.Actor = domain.SanitizeEvidence(override.Actor)
	override.Reason = domain.SanitizeEvidence(override.Reason)
	_, err := r.pool.Exec(ctx, `INSERT INTO managed_instance_overrides (`+maintenanceOverrideColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (service_id, environment_id, deployment_unit_id, runtime_target_name) DO UPDATE SET
			id = EXCLUDED.id, actor = EXCLUDED.actor, reason = EXCLUDED.reason,
			created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at`, override.ID, override.ServiceID,
		override.EnvironmentID, nullableDeploymentUnitID(override.DeploymentUnitID), override.RuntimeTargetName, override.Actor,
		override.Reason, override.CreatedAt, override.ExpiresAt)
	if err != nil {
		return fmt.Errorf("creating managed instance maintenance override: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) ClearMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM managed_instance_overrides
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id IS NOT DISTINCT FROM $3 AND runtime_target_name = $4`,
		key.ServiceID, key.EnvironmentID, nullableDeploymentUnitID(key.DeploymentUnitID), key.RuntimeTargetName)
	if err != nil {
		return fmt.Errorf("clearing managed instance maintenance override: %w", err)
	}
	return nil
}

func (r *PgManagedInstanceHealthRepository) GetActiveMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+maintenanceOverrideColumns+` FROM managed_instance_overrides
		WHERE service_id = $1 AND environment_id = $2 AND deployment_unit_id IS NOT DISTINCT FROM $3 AND runtime_target_name = $4
		AND created_at <= $5 AND (expires_at IS NULL OR expires_at > $5)`, key.ServiceID, key.EnvironmentID,
		nullableDeploymentUnitID(key.DeploymentUnitID), key.RuntimeTargetName, at)
	var override domain.MaintenanceOverride
	var deploymentUnitID pgtype.UUID
	if err := row.Scan(&override.ID, &override.ServiceID, &override.EnvironmentID, &deploymentUnitID,
		&override.RuntimeTargetName, &override.Actor, &override.Reason, &override.CreatedAt, &override.ExpiresAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying active managed instance maintenance override: %w", err)
	}
	if deploymentUnitID.Valid {
		override.DeploymentUnitID = uuid.UUID(deploymentUnitID.Bytes)
	}
	return &override, nil
}

func nullableDeploymentUnitID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func boundedHealthListLimit(limit int) int {
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

func nonNegativeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
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
