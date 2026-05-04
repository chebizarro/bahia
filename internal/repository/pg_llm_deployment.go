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

// PgLLMDeploymentIntentRepository is a PostgreSQL implementation.
type PgLLMDeploymentIntentRepository struct {
	pool pgQueryer
}

func NewPgLLMDeploymentIntentRepository(pool *pgxpool.Pool) *PgLLMDeploymentIntentRepository {
	return newPgLLMDeploymentIntentRepositoryWithDB(pool)
}

func newPgLLMDeploymentIntentRepositoryWithDB(db pgQueryer) *PgLLMDeploymentIntentRepository {
	return &PgLLMDeploymentIntentRepository{pool: db}
}

const llmIntentColumns = `id, route_id, environment_id, release_id, requested_by, source_kind, approval_status, status, supersedes_intent_id, approval_metadata, metadata, created_at, approved_at, updated_at`

func (r *PgLLMDeploymentIntentRepository) Create(ctx context.Context, intent *domain.LLMDeploymentIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	now := time.Now().UTC()
	intent.CreatedAt = now
	intent.UpdatedAt = now

	approvalJSON, err := marshalJSON(intent.ApprovalMetadata, "LLM intent approval metadata")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(intent.Metadata, "LLM intent metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_deployment_intents (`+llmIntentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, intent.ID, intent.RouteID, intent.EnvironmentID, intent.ReleaseID, intent.RequestedBy, intent.SourceKind,
		intent.ApprovalStatus, intent.Status, intent.SupersedesIntentID, approvalJSON, metaJSON,
		intent.CreatedAt, intent.ApprovedAt, intent.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting LLM deployment intent: %w", err)
	}
	return nil
}

func (r *PgLLMDeploymentIntentRepository) scanIntent(row pgx.Row) (*domain.LLMDeploymentIntent, error) {
	intent := &domain.LLMDeploymentIntent{}
	var approvalJSON, metaJSON []byte
	if err := row.Scan(&intent.ID, &intent.RouteID, &intent.EnvironmentID, &intent.ReleaseID, &intent.RequestedBy,
		&intent.SourceKind, &intent.ApprovalStatus, &intent.Status, &intent.SupersedesIntentID,
		&approvalJSON, &metaJSON, &intent.CreatedAt, &intent.ApprovedAt, &intent.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(approvalJSON, &intent.ApprovalMetadata, "LLM intent approval metadata"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &intent.Metadata, "LLM intent metadata"); err != nil {
		return nil, err
	}
	return intent, nil
}

func (r *PgLLMDeploymentIntentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	intent, err := r.scanIntent(r.pool.QueryRow(ctx, `SELECT `+llmIntentColumns+` FROM llm_deployment_intents WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM deployment intent by id: %w", err)
	}
	return intent, nil
}

func (r *PgLLMDeploymentIntentRepository) ListByRouteEnv(ctx context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+llmIntentColumns+` FROM llm_deployment_intents
		WHERE route_id = $1 AND environment_id = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4
	`, routeID, envID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing LLM deployment intents: %w", err)
	}
	defer rows.Close()

	var intents []domain.LLMDeploymentIntent
	for rows.Next() {
		intent, err := r.scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM deployment intent: %w", err)
		}
		intents = append(intents, *intent)
	}
	return intents, rows.Err()
}

func (r *PgLLMDeploymentIntentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE llm_deployment_intents SET status = $2, updated_at = $3 WHERE id = $1`, id, status, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating LLM deployment intent status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating LLM deployment intent %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgLLMDeploymentIntentRepository) UpdateApproval(ctx context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	now := time.Now().UTC()
	var approvedAt *time.Time
	if status == domain.ApprovalStatusApproved {
		approvedAt = &now
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE llm_deployment_intents SET approval_status = $2, approved_at = $3, updated_at = $4 WHERE id = $1`, id, status, approvedAt, now)
	if err != nil {
		return fmt.Errorf("updating LLM deployment intent approval: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("approving LLM deployment intent %s: %w", id, ErrNotFound)
	}
	return nil
}

// PgLLMDeploymentRunRepository is a PostgreSQL implementation.
type PgLLMDeploymentRunRepository struct {
	pool pgQueryer
}

func NewPgLLMDeploymentRunRepository(pool *pgxpool.Pool) *PgLLMDeploymentRunRepository {
	return newPgLLMDeploymentRunRepositoryWithDB(pool)
}

func newPgLLMDeploymentRunRepositoryWithDB(db pgQueryer) *PgLLMDeploymentRunRepository {
	return &PgLLMDeploymentRunRepository{pool: db}
}

const llmRunColumns = `id, deployment_intent_id, backend_kind, endpoint_ref, worker_pubkey, worker_name, backend_endpoint, status, exit_code, stdout_ref, stderr_ref, metadata, started_at, finished_at, created_at, updated_at`

func (r *PgLLMDeploymentRunRepository) Create(ctx context.Context, run *domain.LLMDeploymentRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now

	metaJSON, err := marshalJSON(run.Metadata, "LLM run metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_deployment_runs (`+llmRunColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`, run.ID, run.DeploymentIntentID, run.BackendKind, run.EndpointRef, run.WorkerPubkey, run.WorkerName,
		run.BackendEndpoint, run.Status, run.ExitCode, run.StdoutRef, run.StderrRef, metaJSON,
		run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting LLM deployment run: %w", err)
	}
	return nil
}

func (r *PgLLMDeploymentRunRepository) scanRun(row pgx.Row) (*domain.LLMDeploymentRun, error) {
	run := &domain.LLMDeploymentRun{}
	var metaJSON []byte
	if err := row.Scan(&run.ID, &run.DeploymentIntentID, &run.BackendKind, &run.EndpointRef, &run.WorkerPubkey,
		&run.WorkerName, &run.BackendEndpoint, &run.Status, &run.ExitCode, &run.StdoutRef, &run.StderrRef,
		&metaJSON, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &run.Metadata, "LLM run metadata"); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *PgLLMDeploymentRunRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	run, err := r.scanRun(r.pool.QueryRow(ctx, `SELECT `+llmRunColumns+` FROM llm_deployment_runs WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM deployment run by id: %w", err)
	}
	return run, nil
}

func (r *PgLLMDeploymentRunRepository) ListByIntent(ctx context.Context, intentID uuid.UUID) ([]domain.LLMDeploymentRun, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+llmRunColumns+` FROM llm_deployment_runs WHERE deployment_intent_id = $1 ORDER BY created_at DESC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("listing LLM deployment runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.LLMDeploymentRun
	for rows.Next() {
		run, err := r.scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM deployment run: %w", err)
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func (r *PgLLMDeploymentRunRepository) EnsureQueuedRunForNextReadyIntent(ctx context.Context) (*domain.LLMDeploymentRun, error) {
	run := &domain.LLMDeploymentRun{}
	now := time.Now().UTC()
	run.ID = uuid.New()
	run.Status = domain.RunStatusQueued
	run.CreatedAt = now
	run.UpdatedAt = now
	created, err := r.scanRun(r.pool.QueryRow(ctx, `
		WITH next_intent AS (
			SELECT i.id, i.route_id, i.release_id, i.environment_id,
			       i.metadata->>'nostr_event_id' AS nostr_event_id,
			       i.metadata->>'nostr_request_pubkey' AS nostr_request_pubkey
			FROM llm_deployment_intents i
			JOIN llm_route_state s
			  ON s.route_id = i.route_id
			 AND s.environment_id = i.environment_id
			 AND s.desired_intent_id = i.id
			WHERE i.status = 'approved'
			  AND NOT EXISTS (
				SELECT 1 FROM llm_deployment_runs r
				WHERE r.deployment_intent_id = i.id
				  AND r.status IN ('queued', 'running')
			  )
			ORDER BY i.created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		INSERT INTO llm_deployment_runs (`+llmRunColumns+`)
		SELECT $1, id, '', '', '', '', '', $2, NULL, '', '', jsonb_build_object(
			'route_id', route_id,
			'release_id', release_id,
			'environment_id', environment_id,
			'nostr_event_id', nostr_event_id,
			'nostr_request_pubkey', nostr_request_pubkey
		), NULL, NULL, $3, $4
		FROM next_intent
		RETURNING `+llmRunColumns+`
	`, run.ID, run.Status, run.CreatedAt, run.UpdatedAt))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("ensuring queued LLM run: %w", err)
	}
	return created, nil
}

func (r *PgLLMDeploymentRunRepository) ClaimNextQueuedRun(ctx context.Context) (*domain.LLMDeploymentRun, error) {
	now := time.Now().UTC()
	run, err := r.scanRun(r.pool.QueryRow(ctx, `
		UPDATE llm_deployment_runs
		SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
		WHERE id = (
			SELECT id FROM llm_deployment_runs
			WHERE status = 'queued'
			ORDER BY created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+llmRunColumns+`
	`, now))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claiming queued LLM run: %w", err)
	}
	return run, nil
}

func (r *PgLLMDeploymentRunRepository) RequeueStaleRunning(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	cmd, err := r.pool.Exec(ctx, `
		UPDATE llm_deployment_runs
		SET status = 'queued', started_at = NULL, updated_at = now()
		WHERE status = 'running' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale LLM runs: %w", err)
	}
	return int(cmd.RowsAffected()), nil
}

func (r *PgLLMDeploymentRunRepository) Update(ctx context.Context, run *domain.LLMDeploymentRun) error {
	run.UpdatedAt = time.Now().UTC()
	metaJSON, err := marshalJSON(run.Metadata, "LLM run metadata")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `
		UPDATE llm_deployment_runs
		SET backend_kind = $2, endpoint_ref = $3, worker_pubkey = $4, worker_name = $5,
			backend_endpoint = $6, status = $7, exit_code = $8, stdout_ref = $9, stderr_ref = $10,
			metadata = $11, started_at = $12, finished_at = $13, updated_at = $14
		WHERE id = $1
	`, run.ID, run.BackendKind, run.EndpointRef, run.WorkerPubkey, run.WorkerName, run.BackendEndpoint,
		run.Status, run.ExitCode, run.StdoutRef, run.StderrRef, metaJSON, run.StartedAt, run.FinishedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating LLM deployment run: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating LLM deployment run %s: %w", run.ID, ErrNotFound)
	}
	return nil
}

func (r *PgLLMDeploymentRunRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	now := time.Now().UTC()
	var finishedAt *time.Time
	if status == domain.RunStatusSucceeded || status == domain.RunStatusFailed || status == domain.RunStatusCancelled || status == domain.RunStatusTimeout {
		finishedAt = &now
	}
	cmd, err := r.pool.Exec(ctx, `
		UPDATE llm_deployment_runs
		SET status = $2, exit_code = $3, finished_at = $4, updated_at = $5
		WHERE id = $1
	`, id, status, exitCode, finishedAt, now)
	if err != nil {
		return fmt.Errorf("updating LLM deployment run status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating LLM deployment run %s: %w", id, ErrNotFound)
	}
	return nil
}
