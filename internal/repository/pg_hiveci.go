package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgHiveCIRepository implements HiveCIRepository using PostgreSQL.
type hiveCIDB interface {
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

func (r *PgHiveCIRepository) UpsertWorkflowRun(ctx context.Context, run domain.HiveCIWorkflowRun) error {
	_, err := r.pool.Exec(ctx, `
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

	_, err = r.pool.Exec(ctx, `
		UPDATE hiveci_workflow_results
		SET processing_state = 'pending_result', updated_at = now()
		WHERE run_event_id = $1 AND processing_state = 'pending_run'
	`, run.RunEventID)
	if err != nil {
		return fmt.Errorf("updating hiveci result state after run upsert: %w", err)
	}

	return nil
}

func (r *PgHiveCIRepository) UpsertWorkflowResult(ctx context.Context, result domain.HiveCIWorkflowResult) error {
	state := result.ProcessingState
	if state == "" {
		state = domain.HiveCIProcessingStatePendingRun
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO hiveci_workflow_results
			(result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, publisher_pubkey, processing_state, processing_error,
				retry_count, last_retry_at, event_created_at, created_at, updated_at)
		VALUES
			($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''),
				$11, $12, NULLIF($13, ''), 0, NULL, $14, now(), now())
		ON CONFLICT (result_event_id) DO NOTHING
	`, result.ResultEventID, result.RunEventID, result.Status, result.ExitCode, result.DurationSeconds,
		result.LogURL, result.Error, result.ImageRepo, result.ImageTag, result.ImageDigest,
		result.PublisherPubkey, string(state), result.ProcessingError, result.EventCreatedAt)
	if err != nil {
		return fmt.Errorf("upserting hiveci workflow result: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
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
		&run.ProcessingError,
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
	return &run, nil
}

func (r *PgHiveCIRepository) GetResultByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowResult, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
		       error, image_repo, image_tag, image_digest, publisher_pubkey, processing_state, processing_error,
		       retry_count, last_retry_at, event_created_at, created_at, updated_at
		FROM hiveci_workflow_results
		WHERE result_event_id = $1
	`, eventID)

	var result domain.HiveCIWorkflowResult
	var state string
	if err := row.Scan(
		&result.ResultEventID,
		&result.RunEventID,
		&result.Status,
		&result.ExitCode,
		&result.DurationSeconds,
		&result.LogURL,
		&result.Error,
		&result.ImageRepo,
		&result.ImageTag,
		&result.ImageDigest,
		&result.PublisherPubkey,
		&state,
		&result.ProcessingError,
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
	result.ProcessingState = domain.HiveCIProcessingState(state)
	return &result, nil
}

func (r *PgHiveCIRepository) ListPendingResults(ctx context.Context) ([]domain.HiveCIWorkflowResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, publisher_pubkey, processing_state, processing_error,
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
		if err := rows.Scan(
			&res.ResultEventID,
			&res.RunEventID,
			&res.Status,
			&res.ExitCode,
			&res.DurationSeconds,
			&res.LogURL,
			&res.Error,
			&res.ImageRepo,
			&res.ImageTag,
			&res.ImageDigest,
			&res.PublisherPubkey,
			&state,
			&res.ProcessingError,
			&res.RetryCount,
			&res.LastRetryAt,
			&res.EventCreatedAt,
			&res.CreatedAt,
			&res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning hiveci result row: %w", err)
		}
		res.ProcessingState = domain.HiveCIProcessingState(state)
		results = append(results, res)
	}

	return results, rows.Err()
}

func (r *PgHiveCIRepository) ListOrphanedResultsByRun(ctx context.Context, runEventID string) ([]domain.HiveCIWorkflowResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT result_event_id, run_event_id, status, exit_code, duration_seconds, log_url,
				error, image_repo, image_tag, image_digest, publisher_pubkey, processing_state, processing_error,
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
		if err := rows.Scan(
			&res.ResultEventID,
			&res.RunEventID,
			&res.Status,
			&res.ExitCode,
			&res.DurationSeconds,
			&res.LogURL,
			&res.Error,
			&res.ImageRepo,
			&res.ImageTag,
			&res.ImageDigest,
			&res.PublisherPubkey,
			&state,
			&res.ProcessingError,
			&res.RetryCount,
			&res.LastRetryAt,
			&res.EventCreatedAt,
			&res.CreatedAt,
			&res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning orphaned hiveci result row: %w", err)
		}
		res.ProcessingState = domain.HiveCIProcessingState(state)
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

func (r *PgHiveCIRepository) LookupRepositoryCI(ctx context.Context, repoCoordinates []string, includeDisabledPolicies bool) ([]domain.RepositoryCILookup, error) {
	if len(repoCoordinates) == 0 {
		return []domain.RepositoryCILookup{}, nil
	}

	uniqueCoordinates := make([]string, 0, len(repoCoordinates))
	seenCoordinates := make(map[string]struct{}, len(repoCoordinates))
	for _, coord := range repoCoordinates {
		if _, seen := seenCoordinates[coord]; seen {
			continue
		}
		seenCoordinates[coord] = struct{}{}
		uniqueCoordinates = append(uniqueCoordinates, coord)
	}

	lookupsByCoordinate := make(map[string]*domain.RepositoryCILookup, len(uniqueCoordinates))
	for _, coord := range uniqueCoordinates {
		lookupsByCoordinate[coord] = &domain.RepositoryCILookup{
			RepoCoordinate: coord,
			Policies:       []domain.RepositoryCIPolicyLink{},
			LinkedServices: []domain.RepositoryCIServiceLink{},
		}
	}

	runRows, err := r.pool.Query(ctx, `
		WITH requested AS (
			SELECT unnest($1::text[]) AS repo_coordinate
		),
		latest_runs AS (
			SELECT DISTINCT ON (r.repo_coordinate)
				r.*
			FROM hiveci_workflow_runs r
			INNER JOIN requested req ON r.repo_coordinate = req.repo_coordinate
			ORDER BY r.repo_coordinate, r.event_created_at DESC, r.created_at DESC, r.run_event_id DESC
		)
		SELECT
			lr.repo_coordinate,
			lr.run_event_id,
			lr.commit_sha,
			lr.branch,
			lr.workflow_path,
			lr.trigger_type,
			lr.triggered_by,
			lr.publisher_pubkey,
			lr.event_created_at AS run_event_created_at,
			lr.processing_state AS run_processing_state,
			res.result_event_id,
			res.status,
			res.exit_code,
			res.duration_seconds,
			res.log_url,
			res.error,
			res.image_repo,
			res.image_tag,
			res.image_digest,
			res.processing_state AS result_processing_state,
			res.processing_error,
			res.retry_count,
			res.last_retry_at,
			res.event_created_at AS result_event_created_at
		FROM latest_runs lr
		LEFT JOIN hiveci_workflow_results res ON res.run_event_id = lr.run_event_id
	`, uniqueCoordinates)
	if err != nil {
		return nil, fmt.Errorf("looking up repository ci latest runs: %w", err)
	}
	defer runRows.Close()

	for runRows.Next() {
		row, err := scanRepositoryCIRunResultRow(runRows)
		if err != nil {
			return nil, err
		}

		lookup := lookupsByCoordinate[row.RepoCoordinate]
		if lookup == nil {
			continue
		}
		if row.RunEventID.Valid {
			lookup.LatestRun = &domain.RepositoryCIRunSummary{
				RunEventID:      row.RunEventID.String,
				WorkflowPath:    row.WorkflowPath.String,
				Branch:          row.Branch.String,
				CommitSHA:       row.CommitSHA.String,
				TriggerType:     row.TriggerType.String,
				TriggeredBy:     row.TriggeredBy.String,
				PublisherPubkey: row.PublisherPubkey.String,
				EventCreatedAt:  row.RunEventCreatedAt.Time,
				ProcessingState: domain.HiveCIProcessingState(row.RunProcessingState.String),
			}
		}
		if row.ResultEventID.Valid {
			lookup.LatestResult = &domain.RepositoryCIResultSummary{
				ResultEventID:   row.ResultEventID.String,
				Status:          row.Status.String,
				ExitCode:        int(row.ExitCode.Int64),
				DurationSeconds: int(row.DurationSeconds.Int64),
				LogURL:          row.LogURL.String,
				Error:           row.Error.String,
				ImageRepo:       row.ImageRepo.String,
				ImageTag:        row.ImageTag.String,
				ImageDigest:     row.ImageDigest.String,
				ProcessingState: domain.HiveCIProcessingState(row.ResultProcessingState.String),
				ProcessingError: row.ProcessingError.String,
				RetryCount:      int(row.RetryCount.Int64),
				EventCreatedAt:  row.ResultEventCreatedAt.Time,
			}
			if row.LastRetryAt.Valid {
				lookup.LatestResult.LastRetryAt = &row.LastRetryAt.Time
			}
		}
	}
	if err := runRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating repository ci latest runs: %w", err)
	}

	policyRows, err := r.pool.Query(ctx, `
		SELECT
			p.id AS policy_id,
			p.repo_coordinate,
			p.workflow_path,
			p.branch_pattern,
			p.enabled,
			p.service_id,
			s.name AS service_name,
			p.environment_id,
			e.name AS environment_name
		FROM hiveci_pipeline_policies p
		INNER JOIN services s ON s.id = p.service_id
		INNER JOIN environments e ON e.id = p.environment_id
		WHERE p.repo_coordinate = ANY($1)
		  AND ($2 OR p.enabled = true)
		ORDER BY p.repo_coordinate, s.name, e.name
	`, uniqueCoordinates, includeDisabledPolicies)
	if err != nil {
		return nil, fmt.Errorf("looking up repository ci policies: %w", err)
	}
	defer policyRows.Close()

	serviceIndexByCoordinate := make(map[string]map[uuid.UUID]int)
	environmentSeenByCoordinateService := make(map[string]map[uuid.UUID]map[uuid.UUID]struct{})
	for policyRows.Next() {
		row, err := scanRepositoryCIPolicyRow(policyRows)
		if err != nil {
			return nil, err
		}

		lookup := lookupsByCoordinate[row.RepoCoordinate]
		if lookup == nil {
			continue
		}

		lookup.Policies = append(lookup.Policies, domain.RepositoryCIPolicyLink{
			PolicyID:        row.PolicyID,
			WorkflowPath:    row.WorkflowPath,
			BranchPattern:   row.BranchPattern.String,
			Enabled:         row.Enabled,
			ServiceID:       row.ServiceID,
			ServiceName:     row.ServiceName,
			EnvironmentID:   row.EnvironmentID,
			EnvironmentName: row.EnvironmentName,
		})

		addRepositoryCILinkedService(
			lookup,
			row,
			serviceIndexByCoordinate,
			environmentSeenByCoordinateService,
		)
	}
	if err := policyRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating repository ci policies: %w", err)
	}

	lookups := make([]domain.RepositoryCILookup, 0, len(uniqueCoordinates))
	for _, coord := range uniqueCoordinates {
		lookups = append(lookups, *lookupsByCoordinate[coord])
	}
	return lookups, nil
}

type runResultRow struct {
	RepoCoordinate string

	RunEventID         sql.NullString
	CommitSHA          sql.NullString
	Branch             sql.NullString
	WorkflowPath       sql.NullString
	TriggerType        sql.NullString
	TriggeredBy        sql.NullString
	PublisherPubkey    sql.NullString
	RunEventCreatedAt  sql.NullTime
	RunProcessingState sql.NullString

	ResultEventID         sql.NullString
	Status                sql.NullString
	ExitCode              sql.NullInt64
	DurationSeconds       sql.NullInt64
	LogURL                sql.NullString
	Error                 sql.NullString
	ImageRepo             sql.NullString
	ImageTag              sql.NullString
	ImageDigest           sql.NullString
	ResultProcessingState sql.NullString
	ProcessingError       sql.NullString
	RetryCount            sql.NullInt64
	LastRetryAt           sql.NullTime
	ResultEventCreatedAt  sql.NullTime
}

type policyRow struct {
	PolicyID        uuid.UUID
	RepoCoordinate  string
	WorkflowPath    string
	BranchPattern   sql.NullString
	Enabled         bool
	ServiceID       uuid.UUID
	ServiceName     string
	EnvironmentID   uuid.UUID
	EnvironmentName string
}

func scanRepositoryCIRunResultRow(row interface{ Scan(...any) error }) (runResultRow, error) {
	var rr runResultRow
	if err := row.Scan(
		&rr.RepoCoordinate,
		&rr.RunEventID,
		&rr.CommitSHA,
		&rr.Branch,
		&rr.WorkflowPath,
		&rr.TriggerType,
		&rr.TriggeredBy,
		&rr.PublisherPubkey,
		&rr.RunEventCreatedAt,
		&rr.RunProcessingState,
		&rr.ResultEventID,
		&rr.Status,
		&rr.ExitCode,
		&rr.DurationSeconds,
		&rr.LogURL,
		&rr.Error,
		&rr.ImageRepo,
		&rr.ImageTag,
		&rr.ImageDigest,
		&rr.ResultProcessingState,
		&rr.ProcessingError,
		&rr.RetryCount,
		&rr.LastRetryAt,
		&rr.ResultEventCreatedAt,
	); err != nil {
		return rr, fmt.Errorf("scanning repository ci run/result row: %w", err)
	}
	return rr, nil
}

func scanRepositoryCIPolicyRow(row interface{ Scan(...any) error }) (policyRow, error) {
	var pr policyRow
	if err := row.Scan(
		&pr.PolicyID,
		&pr.RepoCoordinate,
		&pr.WorkflowPath,
		&pr.BranchPattern,
		&pr.Enabled,
		&pr.ServiceID,
		&pr.ServiceName,
		&pr.EnvironmentID,
		&pr.EnvironmentName,
	); err != nil {
		return pr, fmt.Errorf("scanning repository ci policy row: %w", err)
	}
	return pr, nil
}

func addRepositoryCILinkedService(
	lookup *domain.RepositoryCILookup,
	row policyRow,
	serviceIndexByCoordinate map[string]map[uuid.UUID]int,
	environmentSeenByCoordinateService map[string]map[uuid.UUID]map[uuid.UUID]struct{},
) {
	serviceIndex, ok := serviceIndexByCoordinate[row.RepoCoordinate]
	if !ok {
		serviceIndex = make(map[uuid.UUID]int)
		serviceIndexByCoordinate[row.RepoCoordinate] = serviceIndex
	}

	environmentSeenByService, ok := environmentSeenByCoordinateService[row.RepoCoordinate]
	if !ok {
		environmentSeenByService = make(map[uuid.UUID]map[uuid.UUID]struct{})
		environmentSeenByCoordinateService[row.RepoCoordinate] = environmentSeenByService
	}

	idx, ok := serviceIndex[row.ServiceID]
	if !ok {
		idx = len(lookup.LinkedServices)
		serviceIndex[row.ServiceID] = idx
		lookup.LinkedServices = append(lookup.LinkedServices, domain.RepositoryCIServiceLink{
			ServiceID:        row.ServiceID,
			ServiceName:      row.ServiceName,
			EnvironmentIDs:   []uuid.UUID{},
			EnvironmentNames: []string{},
		})
	}

	envSeen, ok := environmentSeenByService[row.ServiceID]
	if !ok {
		envSeen = make(map[uuid.UUID]struct{})
		environmentSeenByService[row.ServiceID] = envSeen
	}
	if _, seen := envSeen[row.EnvironmentID]; seen {
		return
	}
	envSeen[row.EnvironmentID] = struct{}{}
	lookup.LinkedServices[idx].EnvironmentIDs = append(lookup.LinkedServices[idx].EnvironmentIDs, row.EnvironmentID)
	lookup.LinkedServices[idx].EnvironmentNames = append(lookup.LinkedServices[idx].EnvironmentNames, row.EnvironmentName)
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
		domain.HiveCIProcessingStateFailed:    {},
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
	if err := row.Scan(
		&p.ID,
		&p.RepoCoordinate,
		&p.WorkflowPath,
		&p.BranchPattern,
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
	if err := unmarshalJSON(metadata, &p.Metadata, "metadata"); err != nil {
		return p, err
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	return p, nil
}
