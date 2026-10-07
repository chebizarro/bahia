package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgHiveCIRepository implements HiveCIRepository using PostgreSQL.
type hiveCIDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PgHiveCIRepository struct {
	pool hiveCIDB
}

// NewPgHiveCIRepository creates a new Hive-CI repository.
func NewPgHiveCIRepository(pool *pgxpool.Pool) *PgHiveCIRepository {
	return &PgHiveCIRepository{pool: pool}
}

func (r *PgHiveCIRepository) withProjectionTx(ctx context.Context, fn func(pgx.Tx) error) (err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning hiveci projection transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed && err == nil {
			err = fmt.Errorf("rolling back hiveci projection transaction: %w", rollbackErr)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing hiveci projection transaction: %w", err)
	}
	committed = true
	return nil
}

func (r *PgHiveCIRepository) UpsertWorkflowRun(ctx context.Context, run domain.HiveCIWorkflowRun) error {
	return r.withProjectionTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO hiveci_workflow_runs
			(run_event_id, repo_coordinate, commit_sha, branch, workflow_path, trigger_type,
			 triggered_by, publisher_pubkey, processing_state, processing_error, event_created_at, created_at, updated_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8,
			 COALESCE(NULLIF($9, ''), 'pending_result'), NULLIF($10, ''), $11, now(), now())
		ON CONFLICT (run_event_id) DO NOTHING
	`, run.RunEventID, run.RepoCoordinate, run.CommitSHA, run.Branch, run.WorkflowPath, run.TriggerType,
			run.TriggeredBy, run.PublisherPubkey, string(run.ProcessingState), run.ProcessingError, run.EventCreatedAt)
		if err != nil {
			return fmt.Errorf("upserting hiveci workflow run: %w", err)
		}

		_, err = tx.Exec(ctx, `
			UPDATE hiveci_workflow_results
			SET processing_state = 'pending_result', updated_at = now()
			WHERE run_event_id = $1 AND processing_state = 'pending_run'
		`, run.RunEventID)
		if err != nil {
			return fmt.Errorf("updating hiveci result state after run upsert: %w", err)
		}
		return nil
	})
}

func (r *PgHiveCIRepository) UpsertWorkflowResult(ctx context.Context, result domain.HiveCIWorkflowResult) error {
	state := result.ProcessingState
	if state == "" {
		state = domain.HiveCIProcessingStatePendingRun
	}

	return r.withProjectionTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO hiveci_workflow_results
			(result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, pstf_gate_name, pstf_gate_status, publisher_pubkey, processing_state, processing_error,
				retry_count, last_retry_at, event_created_at, created_at, updated_at)
		VALUES
			($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''),
				NULLIF($11, ''), NULLIF($12, ''), $13, $14, NULLIF($15, ''), 0, NULL, $16, now(), now())
		ON CONFLICT (result_event_id) DO NOTHING
	`, result.ResultEventID, result.RunEventID, result.Status, result.ExitCode, result.DurationSeconds,
			result.LogURL, result.Error, result.ImageRepo, result.ImageTag, result.ImageDigest,
			result.PSTFGateName, result.PSTFGateStatus, result.PublisherPubkey, string(state), result.ProcessingError, result.EventCreatedAt)
		if err != nil {
			return fmt.Errorf("upserting hiveci workflow result: %w", err)
		}

		_, err = tx.Exec(ctx, `
		UPDATE hiveci_workflow_results r
		SET processing_state = 'pending_result', updated_at = now()
		WHERE r.run_event_id = $1
		  AND r.processing_state = 'pending_run'
		  AND EXISTS (SELECT 1 FROM hiveci_workflow_runs w WHERE w.run_event_id = r.run_event_id)
	`, result.RunEventID)
		if err != nil {
			return fmt.Errorf("updating hiveci result state after result upsert: %w", err)
		}
		return nil
	})
}

func (r *PgHiveCIRepository) GetRunByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowRun, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT run_event_id, repo_coordinate, commit_sha, branch, workflow_path, trigger_type,
		       triggered_by, publisher_pubkey, processing_state, processing_error,
		       event_created_at, created_at, updated_at
		FROM hiveci_workflow_runs
		WHERE run_event_id = $1
	`, eventID)

	var run domain.HiveCIWorkflowRun
	var state string
	var processingError sql.NullString
	if err := row.Scan(
		&run.RunEventID,
		&run.RepoCoordinate,
		&run.CommitSHA,
		&run.Branch,
		&run.WorkflowPath,
		&run.TriggerType,
		&run.TriggeredBy,
		&run.PublisherPubkey,
		&state,
		&processingError,
		&run.EventCreatedAt,
		&run.CreatedAt,
		&run.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting hiveci workflow run: %w", err)
	}
	run.ProcessingState = domain.HiveCIProcessingState(state)
	run.ProcessingError = nullStringValue(processingError)
	return &run, nil
}

func (r *PgHiveCIRepository) FindWorkflowRun(ctx context.Context, repoCoordinate, commitSHA, workflowPath string) (*domain.HiveCIWorkflowRun, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT run_event_id, repo_coordinate, commit_sha, branch, workflow_path, trigger_type,
		triggered_by, publisher_pubkey, processing_state, processing_error,
		event_created_at, created_at, updated_at
		FROM hiveci_workflow_runs
		WHERE repo_coordinate = $1 AND lower(commit_sha) = lower($2) AND workflow_path = $3
		ORDER BY event_created_at DESC, created_at DESC
		LIMIT 1
	`, strings.TrimSpace(repoCoordinate), strings.TrimSpace(commitSHA), strings.TrimSpace(workflowPath))

	var run domain.HiveCIWorkflowRun
	var state string
	var processingError sql.NullString
	if err := row.Scan(
		&run.RunEventID,
		&run.RepoCoordinate,
		&run.CommitSHA,
		&run.Branch,
		&run.WorkflowPath,
		&run.TriggerType,
		&run.TriggeredBy,
		&run.PublisherPubkey,
		&state,
		&processingError,
		&run.EventCreatedAt,
		&run.CreatedAt,
		&run.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("finding hiveci workflow run by identity tuple: %w", err)
	}
	run.ProcessingState = domain.HiveCIProcessingState(state)
	run.ProcessingError = nullStringValue(processingError)
	return &run, nil
}

func (r *PgHiveCIRepository) GetResultByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowResult, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
		       error, image_repo, image_tag, image_digest, pstf_gate_name, pstf_gate_status, publisher_pubkey, processing_state, processing_error,
		       retry_count, last_retry_at, event_created_at, created_at, updated_at
		FROM hiveci_workflow_results
		WHERE result_event_id = $1
	`, eventID)

	var result domain.HiveCIWorkflowResult
	var state string
	var errText sql.NullString
	var imageRepo sql.NullString
	var imageTag sql.NullString
	var imageDigest sql.NullString
	var pstfGateName sql.NullString
	var pstfGateStatus sql.NullString
	var processingError sql.NullString
	if err := row.Scan(
		&result.ResultEventID,
		&result.RunEventID,
		&result.Status,
		&result.ExitCode,
		&result.DurationSeconds,
		&result.LogURL,
		&errText,
		&imageRepo,
		&imageTag,
		&imageDigest,
		&pstfGateName,
		&pstfGateStatus,
		&result.PublisherPubkey,
		&state,
		&processingError,
		&result.RetryCount,
		&result.LastRetryAt,
		&result.EventCreatedAt,
		&result.CreatedAt,
		&result.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting hiveci workflow result: %w", err)
	}
	result.Error = nullStringValue(errText)
	result.ImageRepo = nullStringValue(imageRepo)
	result.ImageTag = nullStringValue(imageTag)
	result.ImageDigest = nullStringValue(imageDigest)
	result.PSTFGateName = nullStringValue(pstfGateName)
	result.PSTFGateStatus = nullStringValue(pstfGateStatus)
	result.ProcessingState = domain.HiveCIProcessingState(state)
	result.ProcessingError = nullStringValue(processingError)
	return &result, nil
}

func (r *PgHiveCIRepository) GetLatestResultByRunEventID(ctx context.Context, runEventID string) (*domain.HiveCIWorkflowResult, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
		error, image_repo, image_tag, image_digest, publisher_pubkey, processing_state, processing_error,
		retry_count, last_retry_at, event_created_at, created_at, updated_at
		FROM hiveci_workflow_results
		WHERE run_event_id = $1
		ORDER BY event_created_at DESC, result_event_id DESC
		LIMIT 1
	`, runEventID)

	var result domain.HiveCIWorkflowResult
	var state string
	var errText, imageRepo, imageTag, imageDigest, processingError sql.NullString
	if err := row.Scan(
		&result.ResultEventID, &result.RunEventID, &result.Status, &result.ExitCode,
		&result.DurationSeconds, &result.LogURL, &errText, &imageRepo, &imageTag,
		&imageDigest, &result.PublisherPubkey, &state, &processingError,
		&result.RetryCount, &result.LastRetryAt, &result.EventCreatedAt,
		&result.CreatedAt, &result.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting latest hiveci result by run: %w", err)
	}
	result.Error = nullStringValue(errText)
	result.ImageRepo = nullStringValue(imageRepo)
	result.ImageTag = nullStringValue(imageTag)
	result.ImageDigest = nullStringValue(imageDigest)
	result.ProcessingState = domain.HiveCIProcessingState(state)
	result.ProcessingError = nullStringValue(processingError)
	return &result, nil
}

func (r *PgHiveCIRepository) ListPendingResults(ctx context.Context) ([]domain.HiveCIWorkflowResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, pstf_gate_name, pstf_gate_status, publisher_pubkey, processing_state, processing_error,
				retry_count, last_retry_at, event_created_at, created_at, updated_at
		FROM hiveci_workflow_results
		WHERE processing_state IN ('pending_result', 'pending_run', 'artifact_pending')
		ORDER BY event_created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing pending hiveci results: %w", err)
	}
	defer rows.Close()

	results := make([]domain.HiveCIWorkflowResult, 0)
	for rows.Next() {
		var res domain.HiveCIWorkflowResult
		var state string
		var errText sql.NullString
		var imageRepo sql.NullString
		var imageTag sql.NullString
		var imageDigest sql.NullString
		var pstfGateName sql.NullString
		var pstfGateStatus sql.NullString
		var processingError sql.NullString
		if err := rows.Scan(
			&res.ResultEventID,
			&res.RunEventID,
			&res.Status,
			&res.ExitCode,
			&res.DurationSeconds,
			&res.LogURL,
			&errText,
			&imageRepo,
			&imageTag,
			&imageDigest,
			&pstfGateName,
			&pstfGateStatus,
			&res.PublisherPubkey,
			&state,
			&processingError,
			&res.RetryCount,
			&res.LastRetryAt,
			&res.EventCreatedAt,
			&res.CreatedAt,
			&res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning hiveci result row: %w", err)
		}
		res.Error = nullStringValue(errText)
		res.ImageRepo = nullStringValue(imageRepo)
		res.ImageTag = nullStringValue(imageTag)
		res.ImageDigest = nullStringValue(imageDigest)
		res.PSTFGateName = nullStringValue(pstfGateName)
		res.PSTFGateStatus = nullStringValue(pstfGateStatus)
		res.ProcessingState = domain.HiveCIProcessingState(state)
		res.ProcessingError = nullStringValue(processingError)
		results = append(results, res)
	}

	return results, rows.Err()
}

func (r *PgHiveCIRepository) ListOrphanedResultsByRun(ctx context.Context, runEventID string) ([]domain.HiveCIWorkflowResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, pstf_gate_name, pstf_gate_status, publisher_pubkey, processing_state, processing_error,
				retry_count, last_retry_at, event_created_at, created_at, updated_at
		FROM hiveci_workflow_results
		WHERE run_event_id = $1 AND processing_state = 'pending_run'
		ORDER BY event_created_at ASC
	`, runEventID)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned hiveci results: %w", err)
	}
	defer rows.Close()

	results := make([]domain.HiveCIWorkflowResult, 0)
	for rows.Next() {
		var res domain.HiveCIWorkflowResult
		var state string
		var errText sql.NullString
		var imageRepo sql.NullString
		var imageTag sql.NullString
		var imageDigest sql.NullString
		var pstfGateName sql.NullString
		var pstfGateStatus sql.NullString
		var processingError sql.NullString
		if err := rows.Scan(
			&res.ResultEventID,
			&res.RunEventID,
			&res.Status,
			&res.ExitCode,
			&res.DurationSeconds,
			&res.LogURL,
			&errText,
			&imageRepo,
			&imageTag,
			&imageDigest,
			&pstfGateName,
			&pstfGateStatus,
			&res.PublisherPubkey,
			&state,
			&processingError,
			&res.RetryCount,
			&res.LastRetryAt,
			&res.EventCreatedAt,
			&res.CreatedAt,
			&res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning orphaned hiveci result row: %w", err)
		}
		res.Error = nullStringValue(errText)
		res.ImageRepo = nullStringValue(imageRepo)
		res.ImageTag = nullStringValue(imageTag)
		res.ImageDigest = nullStringValue(imageDigest)
		res.PSTFGateName = nullStringValue(pstfGateName)
		res.PSTFGateStatus = nullStringValue(pstfGateStatus)
		res.ProcessingState = domain.HiveCIProcessingState(state)
		res.ProcessingError = nullStringValue(processingError)
		results = append(results, res)
	}
	return results, rows.Err()
}

func (r *PgHiveCIRepository) UpdateResultState(ctx context.Context, eventID string, newState domain.HiveCIProcessingState) error {
	if !isValidHiveCIResultTransition(ctx, r.pool, eventID, newState) {
		return fmt.Errorf("invalid hiveci result state transition to %q", newState)
	}

	cmd, err := r.pool.Exec(ctx, `
		UPDATE hiveci_workflow_results
		SET processing_state = $2, updated_at = now()
		WHERE result_event_id = $1
	`, eventID, string(newState))
	if err != nil {
		return fmt.Errorf("updating hiveci result state: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return nil
	}
	return nil
}

func (r *PgHiveCIRepository) IncrementResultRetry(ctx context.Context, eventID string, at time.Time) (int, error) {
	var retryCount int
	if err := r.pool.QueryRow(ctx, `
		UPDATE hiveci_workflow_results
		SET retry_count = retry_count + 1,
		    last_retry_at = $2,
		    updated_at = now()
		WHERE result_event_id = $1
		RETURNING retry_count
	`, eventID, at).Scan(&retryCount); err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("incrementing hiveci result retry: %w", err)
	}
	return retryCount, nil
}

func (r *PgHiveCIRepository) MarkResultFailed(ctx context.Context, eventID, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE hiveci_workflow_results
		SET processing_state = 'failed',
		    processing_error = NULLIF($2, ''),
		    updated_at = now()
		WHERE result_event_id = $1
	`, eventID, reason)
	if err != nil {
		return fmt.Errorf("marking hiveci result failed: %w", err)
	}
	return nil
}

func (r *PgHiveCIRepository) ListPolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, repo_coordinate, workflow_path, branch_pattern, service_id, environment_id,
		       enabled, metadata, created_at, updated_at
		FROM hiveci_pipeline_policies
		ORDER BY repo_coordinate, workflow_path, branch_pattern
	`)
	if err != nil {
		return nil, fmt.Errorf("listing hiveci policies: %w", err)
	}
	defer rows.Close()

	policies := make([]domain.HiveCIPipelinePolicy, 0)
	for rows.Next() {
		p, err := scanHiveCIPipelinePolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, p)
	}

	return policies, rows.Err()
}

func (r *PgHiveCIRepository) GetPolicyByRepoAndWorkflow(ctx context.Context, repo, workflow string) (*domain.HiveCIPipelinePolicy, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, repo_coordinate, workflow_path, branch_pattern, service_id, environment_id,
		       enabled, metadata, created_at, updated_at
		FROM hiveci_pipeline_policies
		WHERE repo_coordinate = $1 AND workflow_path = $2 AND enabled = true
		ORDER BY branch_pattern NULLS FIRST, updated_at DESC
		LIMIT 1
	`, repo, workflow)

	policy, err := scanHiveCIPipelinePolicy(row)
	if err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &policy, nil
}

func (r *PgHiveCIRepository) EnsurePipelinePolicy(ctx context.Context, policy domain.HiveCIPipelinePolicy) error {
	// Reconcile config-owned policy state in place, or insert it when absent.
	// This lets upgrades replace formerly permissive metadata with mandatory
	// release constraints. COALESCE preserves NULL branch-pattern equality.
	metadata := policy.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := marshalJSON(metadata, "metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		WITH reconciled AS (
			UPDATE hiveci_pipeline_policies
			SET enabled = $6, metadata = $7, updated_at = now()
			WHERE repo_coordinate = $1
			AND workflow_path = $2
			AND COALESCE(branch_pattern, '') = COALESCE(NULLIF($3, ''), '')
			AND service_id = $4
			AND environment_id = $5
			RETURNING id
		)
		INSERT INTO hiveci_pipeline_policies
			(repo_coordinate, workflow_path, branch_pattern, service_id, environment_id, enabled, metadata)
		SELECT $1, $2, NULLIF($3, ''), $4, $5, $6, $7
		WHERE NOT EXISTS (SELECT 1 FROM reconciled)
		AND NOT EXISTS (
			SELECT 1 FROM hiveci_pipeline_policies
			WHERE repo_coordinate = $1
			AND workflow_path = $2
			AND COALESCE(branch_pattern, '') = COALESCE(NULLIF($3, ''), '')
			AND service_id = $4
			AND environment_id = $5
		)
	`, policy.RepoCoordinate, policy.WorkflowPath, policy.BranchPattern,
		policy.ServiceID, policy.EnvironmentID, policy.Enabled, metadataJSON)
	if err != nil {
		return fmt.Errorf("ensuring hiveci pipeline policy: %w", err)
	}
	return nil
}

func isValidHiveCIResultTransition(ctx context.Context, pool hiveCIDB, eventID string, newState domain.HiveCIProcessingState) bool {
	if newState == "" {
		return false
	}

	var current string
	err := pool.QueryRow(ctx, `SELECT processing_state FROM hiveci_workflow_results WHERE result_event_id = $1`, eventID).Scan(&current)
	if err != nil {
		return err == pgx.ErrNoRows
	}

	allowed := map[domain.HiveCIProcessingState]map[domain.HiveCIProcessingState]bool{
		domain.HiveCIProcessingStatePendingRun: {
			domain.HiveCIProcessingStatePendingResult: true,
			domain.HiveCIProcessingStateRejected:      true,
			domain.HiveCIProcessingStateFailed:        true,
		},
		domain.HiveCIProcessingStatePendingResult: {
			domain.HiveCIProcessingStateProcessed:       true,
			domain.HiveCIProcessingStateVerified:        true,
			domain.HiveCIProcessingStateArtifactPending: true,
			domain.HiveCIProcessingStateRejected:        true,
			domain.HiveCIProcessingStateFailed:          true,
		},
		domain.HiveCIProcessingStateVerified: {
			domain.HiveCIProcessingStateArtifactPending: true,
			domain.HiveCIProcessingStateProcessed:       true,
			domain.HiveCIProcessingStateRejected:        true,
			domain.HiveCIProcessingStateFailed:          true,
		},
		domain.HiveCIProcessingStateArtifactPending: {
			domain.HiveCIProcessingStateProcessed: true,
			domain.HiveCIProcessingStateFailed:    true,
		},
		domain.HiveCIProcessingStateProcessed: {},
		domain.HiveCIProcessingStateRejected:  {},
		// Operator recovery of a result whose automatic attempts were exhausted
		// (domain.HiveCIProcessingState.CanTransitionTo).
		domain.HiveCIProcessingStateFailed: {
			domain.HiveCIProcessingStateArtifactPending: true,
			domain.HiveCIProcessingStateProcessed:       true,
			domain.HiveCIProcessingStateRejected:        true,
		},
	}

	currentState := domain.HiveCIProcessingState(current)
	if currentState == newState {
		return true
	}
	return allowed[currentState][newState]
}

func scanHiveCIPipelinePolicy(row interface{ Scan(...any) error }) (domain.HiveCIPipelinePolicy, error) {
	var p domain.HiveCIPipelinePolicy
	var metadata []byte
	var branchPattern *string
	if err := row.Scan(
		&p.ID,
		&p.RepoCoordinate,
		&p.WorkflowPath,
		&branchPattern,
		&p.ServiceID,
		&p.EnvironmentID,
		&p.Enabled,
		&metadata,
		&p.CreatedAt,
		&p.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return p, ErrNotFound
		}
		return p, fmt.Errorf("scanning hiveci policy: %w", err)
	}
	if branchPattern != nil {
		p.BranchPattern = *branchPattern
	}
	if err := unmarshalJSON(metadata, &p.Metadata, "metadata"); err != nil {
		return p, err
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	return p, nil
}
