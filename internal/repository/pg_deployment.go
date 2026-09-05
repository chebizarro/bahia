package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgDeploymentIntentRepository is a PostgreSQL implementation.
type deploymentDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PgDeploymentIntentRepository struct {
	pool deploymentDB
}

func NewPgDeploymentIntentRepository(pool *pgxpool.Pool) *PgDeploymentIntentRepository {
	return &PgDeploymentIntentRepository{pool: pool}
}

const intentColumns = `id, service_id, environment_id, deployment_unit_id, artifact_id, requested_by, source_kind, approval_status, status, supersedes_intent_id, approval_metadata, metadata, desired_state, desired_hash, created_at, approved_at, updated_at`

func (r *PgDeploymentIntentRepository) Create(ctx context.Context, di *domain.DeploymentIntent) error {
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	now := time.Now().UTC()
	di.CreatedAt = now
	di.UpdatedAt = now

	approvalJSON, err := marshalJSON(di.ApprovalMetadata, "approval metadata")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(di.Metadata, "intent metadata")
	if err != nil {
		return err
	}
	desiredStateJSON, err := marshalJSON(di.DesiredState, "desired state")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO deployment_intents (`+intentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, di.ID, di.ServiceID, di.EnvironmentID, di.DeploymentUnitID, di.ArtifactID, di.RequestedBy, di.SourceKind, di.ApprovalStatus, di.Status,
		di.SupersedesIntentID, approvalJSON, metaJSON, desiredStateJSON, di.DesiredHash, di.CreatedAt, di.ApprovedAt, di.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting deployment intent: %w", err)
	}
	return nil
}

func (r *PgDeploymentIntentRepository) scanIntent(row pgx.Row) (*domain.DeploymentIntent, error) {
	di := &domain.DeploymentIntent{}
	var approvalJSON, metaJSON, desiredStateJSON []byte
	var desiredHash sql.NullString
	err := row.Scan(&di.ID, &di.ServiceID, &di.EnvironmentID, &di.DeploymentUnitID, &di.ArtifactID, &di.RequestedBy, &di.SourceKind,
		&di.ApprovalStatus, &di.Status, &di.SupersedesIntentID, &approvalJSON, &metaJSON,
		&desiredStateJSON, &desiredHash, &di.CreatedAt, &di.ApprovedAt, &di.UpdatedAt)
	if err != nil {
		return nil, err
	}
	di.DesiredHash = nullStringValue(desiredHash)
	if err := unmarshalJSON(approvalJSON, &di.ApprovalMetadata, "approval metadata"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &di.Metadata, "intent metadata"); err != nil {
		return nil, err
	}
	if len(desiredStateJSON) > 0 && string(desiredStateJSON) != "null" {
		di.DesiredState = &domain.DesiredServiceSpec{}
		if err := unmarshalJSON(desiredStateJSON, di.DesiredState, "desired state"); err != nil {
			return nil, err
		}
	}
	return di, nil
}

func (r *PgDeploymentIntentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+intentColumns+` FROM deployment_intents WHERE id = $1`, id)
	di, err := r.scanIntent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying deployment intent by id: %w", err)
	}
	return di, nil
}

func (r *PgDeploymentIntentRepository) GetByReleasePromotionKey(
	ctx context.Context,
	serviceID, environmentID uuid.UUID,
	requester, idempotencyKey string,
) (*domain.DeploymentIntent, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+intentColumns+` FROM deployment_intents
		WHERE service_id = $1
		AND environment_id = $2
		AND metadata->>'release_promotion' = 'true'
		AND metadata->>'promotion_requester' = $3
		AND metadata->>'promotion_idempotency_key' = $4
		LIMIT 1
	`, serviceID, environmentID, requester, idempotencyKey)
	di, err := r.scanIntent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying release promotion idempotency key: %w", err)
	}
	return di, nil
}

func (r *PgDeploymentIntentRepository) GetByHiveResultEventID(ctx context.Context, eventID string) (*domain.DeploymentIntent, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+intentColumns+` FROM deployment_intents
		WHERE metadata->>'hive_ci_result_event_id' = $1
		LIMIT 1
	`, eventID)
	di, err := r.scanIntent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying deployment intent by hive result event id: %w", err)
	}
	return di, nil
}

func (r *PgDeploymentIntentRepository) ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentColumns+` FROM deployment_intents
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4
	`, serviceID, envID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing deployment intents: %w", err)
	}
	defer rows.Close()

	var intents []domain.DeploymentIntent
	for rows.Next() {
		var di domain.DeploymentIntent
		var approvalJSON, metaJSON, desiredStateJSON []byte
		var desiredHash sql.NullString
		if err := rows.Scan(&di.ID, &di.ServiceID, &di.EnvironmentID, &di.DeploymentUnitID, &di.ArtifactID, &di.RequestedBy, &di.SourceKind,
			&di.ApprovalStatus, &di.Status, &di.SupersedesIntentID, &approvalJSON, &metaJSON,
			&desiredStateJSON, &desiredHash, &di.CreatedAt, &di.ApprovedAt, &di.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning deployment intent: %w", err)
		}
		di.DesiredHash = nullStringValue(desiredHash)
		if err := unmarshalJSON(approvalJSON, &di.ApprovalMetadata, "approval metadata"); err != nil {
			return nil, fmt.Errorf("reading intent %s: %w", di.ID, err)
		}
		if err := unmarshalJSON(metaJSON, &di.Metadata, "intent metadata"); err != nil {
			return nil, fmt.Errorf("reading intent %s: %w", di.ID, err)
		}
		if len(desiredStateJSON) > 0 && string(desiredStateJSON) != "null" {
			di.DesiredState = &domain.DesiredServiceSpec{}
			if err := unmarshalJSON(desiredStateJSON, di.DesiredState, "desired state"); err != nil {
				return nil, fmt.Errorf("reading intent %s desired state: %w", di.ID, err)
			}
		}
		intents = append(intents, di)
	}
	return intents, rows.Err()
}

// ListApprovedWithoutRuns returns approved intents whose execution never acquired
// a durable run, covering the crash window between approval publication and run creation.
func (r *PgDeploymentIntentRepository) ListApprovedWithoutRuns(ctx context.Context) ([]domain.DeploymentIntent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentColumns+` FROM deployment_intents di
		WHERE di.approval_status IN ('approved', 'not_required')
		  AND di.status = 'approved'
		  AND NOT EXISTS (
			SELECT 1 FROM deployment_runs dr WHERE dr.deployment_intent_id = di.id
		  )
		ORDER BY di.approved_at ASC NULLS LAST, di.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing approved deployment intents without runs: %w", err)
	}
	defer rows.Close()

	var intents []domain.DeploymentIntent
	for rows.Next() {
		di, scanErr := r.scanIntent(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scanning approved deployment intent without run: %w", scanErr)
		}
		intents = append(intents, *di)
	}
	return intents, rows.Err()
}

// TransitionDecision atomically moves a still-pending intent to one terminal
// approval decision so concurrent approve/reject requests cannot both win.
func (r *PgDeploymentIntentRepository) TransitionDecision(
	ctx context.Context,
	id uuid.UUID,
	approval domain.ApprovalStatus,
	status domain.DeploymentIntentStatus,
) (bool, error) {
	now := time.Now().UTC()
	var approvedAt *time.Time
	if approval == domain.ApprovalStatusApproved {
		approvedAt = &now
	}
	cmd, err := r.pool.Exec(ctx, `
		UPDATE deployment_intents
		SET approval_status = $2, status = $3, approved_at = $4, updated_at = $5
		WHERE id = $1 AND approval_status = 'pending' AND status = 'pending'
	`, id, approval, status, approvedAt, now)
	if err != nil {
		return false, fmt.Errorf("transitioning deployment intent decision: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

func (r *PgDeploymentIntentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE deployment_intents SET status = $2, updated_at = $3 WHERE id = $1`, id, status, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating deployment intent status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating deployment intent %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgDeploymentIntentRepository) UpdateApproval(ctx context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	now := time.Now().UTC()
	var approvedAt *time.Time
	if status == domain.ApprovalStatusApproved {
		approvedAt = &now
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE deployment_intents SET approval_status = $2, approved_at = $3, updated_at = $4 WHERE id = $1`, id, status, approvedAt, now)
	if err != nil {
		return fmt.Errorf("updating deployment intent approval: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("approving deployment intent %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgDeploymentIntentRepository) UpdateDesiredState(ctx context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	desiredStateJSON, err := marshalJSON(desiredState, "desired state")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE deployment_intents SET desired_state = $2, desired_hash = $3, updated_at = $4 WHERE id = $1`, id, desiredStateJSON, desiredHash, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating deployment intent desired state: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating deployment intent %s desired state: %w", id, ErrNotFound)
	}
	return nil
}

// PgDeploymentRunRepository is a PostgreSQL implementation.
type PgDeploymentRunRepository struct {
	pool deploymentDB
}

func NewPgDeploymentRunRepository(pool *pgxpool.Pool) *PgDeploymentRunRepository {
	return &PgDeploymentRunRepository{pool: pool}
}

const runColumns = `id, deployment_intent_id, deployment_unit_id, loom_job_id, worker_pubkey, worker_name, status, exit_code, stdout_ref, stderr_ref, started_at, finished_at, metadata, apply_metadata, created_at, updated_at`

func (r *PgDeploymentRunRepository) Create(ctx context.Context, dr *domain.DeploymentRun) error {
	if dr.ID == uuid.Nil {
		dr.ID = uuid.New()
	}
	now := time.Now().UTC()
	dr.CreatedAt = now
	dr.UpdatedAt = now

	metaJSON, err := marshalJSON(dr.Metadata, "run metadata")
	if err != nil {
		return err
	}
	applyMetaJSON, err := marshalJSON(dr.ApplyMetadata, "apply metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO deployment_runs (`+runColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`, dr.ID, dr.DeploymentIntentID, dr.DeploymentUnitID, dr.LoomJobID, dr.WorkerPubkey, dr.WorkerName, dr.Status,
		dr.ExitCode, dr.StdoutRef, dr.StderrRef, dr.StartedAt, dr.FinishedAt, metaJSON, applyMetaJSON, dr.CreatedAt, dr.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("deployment intent already has an active run: %w", ErrConflict)
		}
		return fmt.Errorf("inserting deployment run: %w", err)
	}
	return nil
}

func (r *PgDeploymentRunRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	dr := &domain.DeploymentRun{}
	var metaJSON, applyMetaJSON []byte
	err := r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM deployment_runs WHERE id = $1`, id).
		Scan(&dr.ID, &dr.DeploymentIntentID, &dr.DeploymentUnitID, &dr.LoomJobID, &dr.WorkerPubkey, &dr.WorkerName, &dr.Status,
			&dr.ExitCode, &dr.StdoutRef, &dr.StderrRef, &dr.StartedAt, &dr.FinishedAt, &metaJSON, &applyMetaJSON, &dr.CreatedAt, &dr.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying deployment run by id: %w", err)
	}
	if err := unmarshalJSON(metaJSON, &dr.Metadata, "run metadata"); err != nil {
		return nil, fmt.Errorf("reading run %s: %w", id, err)
	}
	if err := unmarshalJSON(applyMetaJSON, &dr.ApplyMetadata, "apply metadata"); err != nil {
		return nil, fmt.Errorf("reading run %s: %w", id, err)
	}
	return dr, nil
}

func (r *PgDeploymentRunRepository) ListByIntent(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+runColumns+` FROM deployment_runs WHERE deployment_intent_id = $1 ORDER BY created_at DESC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("listing deployment runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.DeploymentRun
	for rows.Next() {
		var dr domain.DeploymentRun
		var metaJSON, applyMetaJSON []byte
		if err := rows.Scan(&dr.ID, &dr.DeploymentIntentID, &dr.DeploymentUnitID, &dr.LoomJobID, &dr.WorkerPubkey, &dr.WorkerName, &dr.Status,
			&dr.ExitCode, &dr.StdoutRef, &dr.StderrRef, &dr.StartedAt, &dr.FinishedAt, &metaJSON, &applyMetaJSON, &dr.CreatedAt, &dr.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning deployment run: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &dr.Metadata, "run metadata"); err != nil {
			return nil, fmt.Errorf("reading run %s: %w", dr.ID, err)
		}
		if err := unmarshalJSON(applyMetaJSON, &dr.ApplyMetadata, "apply metadata"); err != nil {
			return nil, fmt.Errorf("reading run %s: %w", dr.ID, err)
		}
		runs = append(runs, dr)
	}
	return runs, rows.Err()
}

// ListNonTerminal returns queued and running deployment runs so workflow
// coordination can reattach to their persisted Loom jobs after a restart.
func (r *PgDeploymentRunRepository) ListNonTerminal(ctx context.Context) ([]domain.DeploymentRun, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+runColumns+` FROM deployment_runs WHERE status IN ('queued', 'running') ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing non-terminal deployment runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.DeploymentRun
	for rows.Next() {
		var dr domain.DeploymentRun
		var metaJSON, applyMetaJSON []byte
		if err := rows.Scan(&dr.ID, &dr.DeploymentIntentID, &dr.DeploymentUnitID, &dr.LoomJobID, &dr.WorkerPubkey, &dr.WorkerName, &dr.Status,
			&dr.ExitCode, &dr.StdoutRef, &dr.StderrRef, &dr.StartedAt, &dr.FinishedAt, &metaJSON, &applyMetaJSON, &dr.CreatedAt, &dr.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning non-terminal deployment run: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &dr.Metadata, "run metadata"); err != nil {
			return nil, fmt.Errorf("reading run %s: %w", dr.ID, err)
		}
		if err := unmarshalJSON(applyMetaJSON, &dr.ApplyMetadata, "apply metadata"); err != nil {
			return nil, fmt.Errorf("reading run %s: %w", dr.ID, err)
		}
		runs = append(runs, dr)
	}
	return runs, rows.Err()
}

func (r *PgDeploymentRunRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	now := time.Now().UTC()
	var finishedAt *time.Time
	if status == domain.RunStatusSucceeded || status == domain.RunStatusFailed || status == domain.RunStatusCancelled || status == domain.RunStatusTimeout {
		finishedAt = &now
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE deployment_runs SET status = $2, exit_code = $3, finished_at = $4, updated_at = $5 WHERE id = $1`, id, status, exitCode, finishedAt, now)
	if err != nil {
		return fmt.Errorf("updating deployment run status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating deployment run %s: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateApplyMetadata persists safe, non-secret deployment progress metadata.
func (r *PgDeploymentRunRepository) UpdateApplyMetadata(ctx context.Context, id uuid.UUID, metadata map[string]any) error {
	metadataJSON, err := marshalJSON(metadata, "run apply metadata")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE deployment_runs SET apply_metadata = $2, updated_at = $3 WHERE id = $1`, id, metadataJSON, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating deployment run apply metadata: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating deployment run %s apply metadata: %w", id, ErrNotFound)
	}
	return nil
}
