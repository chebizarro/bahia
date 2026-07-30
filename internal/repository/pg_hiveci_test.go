package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
)

func TestPgHiveCIRepository_LookupRepositoryCIEmptyInput(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()

	repo := &PgHiveCIRepository{pool: mock}
	lookups, err := repo.LookupRepositoryCI(ctx, nil, false)
	if err != nil {
		t.Fatalf("lookup empty input: %v", err)
	}
	if len(lookups) != 0 {
		t.Fatalf("expected empty lookup result, got %d", len(lookups))
	}
	if lookups == nil {
		t.Fatal("expected non-nil empty lookup slice")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPgHiveCIRepository_LookupRepositoryCIDedupesAndAssembles(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()

	repo := &PgHiveCIRepository{pool: mock}
	now := time.Now().UTC().Truncate(time.Second)
	lastRetryAt := now.Add(5 * time.Minute)
	policyID1 := uuid.New()
	policyID2 := uuid.New()
	policyID3 := uuid.New()
	serviceID1 := uuid.New()
	serviceID2 := uuid.New()
	environmentID1 := uuid.New()
	environmentID2 := uuid.New()

	runColumns := []string{
		"repo_coordinate",
		"run_event_id",
		"commit_sha",
		"branch",
		"workflow_path",
		"trigger_type",
		"triggered_by",
		"publisher_pubkey",
		"run_event_created_at",
		"run_processing_state",
		"result_event_id",
		"status",
		"exit_code",
		"duration_seconds",
		"log_url",
		"error",
		"image_repo",
		"image_tag",
		"image_digest",
		"pstf_gate_name",
		"pstf_gate_status",
		"result_processing_state",
		"processing_error",
		"retry_count",
		"last_retry_at",
		"result_event_created_at",
	}
	mock.ExpectQuery("WITH requested").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(runColumns).
			AddRow(
				"repo-a", "run-a", "sha-a", "main", ".github/workflows/ci.yml", "push", "alice", "pub-a", now, "pending_result",
				"res-a", "success", 0, 42, "https://logs.example/a", "", "ghcr.io/acme/app", "main", "sha256:abc", "pstf-drift", "green", "processed", "", 1, lastRetryAt, now.Add(time.Minute),
			).
			AddRow(
				"repo-b", "run-b", "sha-b", "dev", ".github/workflows/test.yml", nil, nil, "pub-b", now.Add(-time.Hour), "pending_result",
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			))

	policyColumns := []string{
		"policy_id",
		"repo_coordinate",
		"workflow_path",
		"branch_pattern",
		"enabled",
		"service_id",
		"service_name",
		"environment_id",
		"environment_name",
	}
	mock.ExpectQuery("hiveci_pipeline_policies").
		WithArgs(pgxmock.AnyArg(), false).
		WillReturnRows(pgxmock.NewRows(policyColumns).
			AddRow(policyID1, "repo-a", ".github/workflows/ci.yml", "main", true, serviceID1, "api", environmentID1, "prod").
			AddRow(policyID2, "repo-a", ".github/workflows/release.yml", "main", true, serviceID1, "api", environmentID1, "prod").
			AddRow(policyID3, "repo-empty", ".github/workflows/ci.yml", nil, true, serviceID2, "worker", environmentID2, "staging"))

	lookups, err := repo.LookupRepositoryCI(ctx, []string{"repo-b", "repo-a", "repo-b", "repo-empty"}, false)
	if err != nil {
		t.Fatalf("lookup repository ci: %v", err)
	}
	if len(lookups) != 3 {
		t.Fatalf("expected 3 unique lookup results, got %d", len(lookups))
	}
	if lookups[0].RepoCoordinate != "repo-b" || lookups[1].RepoCoordinate != "repo-a" || lookups[2].RepoCoordinate != "repo-empty" {
		t.Fatalf("unexpected lookup order: %#v", []string{lookups[0].RepoCoordinate, lookups[1].RepoCoordinate, lookups[2].RepoCoordinate})
	}

	if lookups[0].LatestRun == nil || lookups[0].LatestRun.RunEventID != "run-b" {
		t.Fatalf("expected repo-b latest run, got %#v", lookups[0].LatestRun)
	}
	if lookups[0].LatestResult != nil {
		t.Fatalf("expected repo-b no latest result, got %#v", lookups[0].LatestResult)
	}

	if lookups[1].LatestRun == nil || lookups[1].LatestRun.RunEventID != "run-a" {
		t.Fatalf("expected repo-a latest run, got %#v", lookups[1].LatestRun)
	}
	if lookups[1].LatestResult == nil || lookups[1].LatestResult.ResultEventID != "res-a" {
		t.Fatalf("expected repo-a latest result, got %#v", lookups[1].LatestResult)
	}
	if lookups[1].LatestResult.LastRetryAt == nil || !lookups[1].LatestResult.LastRetryAt.Equal(lastRetryAt) {
		t.Fatalf("expected repo-a last retry time %v, got %#v", lastRetryAt, lookups[1].LatestResult.LastRetryAt)
	}
	if len(lookups[1].Policies) != 2 {
		t.Fatalf("expected repo-a two policies, got %d", len(lookups[1].Policies))
	}
	if len(lookups[1].LinkedServices) != 1 {
		t.Fatalf("expected repo-a one linked service, got %d", len(lookups[1].LinkedServices))
	}
	if got := len(lookups[1].LinkedServices[0].EnvironmentIDs); got != 1 {
		t.Fatalf("expected duplicate repo-a service environments deduped to one, got %d", got)
	}

	if lookups[2].LatestRun != nil || lookups[2].LatestResult != nil {
		t.Fatalf("expected repo-empty no latest run/result, got %#v / %#v", lookups[2].LatestRun, lookups[2].LatestResult)
	}
	if len(lookups[2].Policies) != 1 || len(lookups[2].LinkedServices) != 1 {
		t.Fatalf("expected repo-empty policies-only entry, got policies=%d services=%d", len(lookups[2].Policies), len(lookups[2].LinkedServices))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPgHiveCIRepository_ResultStateTransitions(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()

	repo := &PgHiveCIRepository{pool: mock}

	mock.ExpectQuery("SELECT processing_state FROM hiveci_workflow_results").
		WithArgs("res-1").
		WillReturnRows(pgxmock.NewRows([]string{"processing_state"}).AddRow("pending_run"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").
		WithArgs("res-1", "pending_result").
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	if err := repo.UpdateResultState(ctx, "res-1", domain.HiveCIProcessingStatePendingResult); err != nil {
		t.Fatalf("pending_run -> pending_result failed: %v", err)
	}

	mock.ExpectQuery("SELECT processing_state FROM hiveci_workflow_results").
		WithArgs("res-1").
		WillReturnRows(pgxmock.NewRows([]string{"processing_state"}).AddRow("pending_result"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").
		WithArgs("res-1", "verified").
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	if err := repo.UpdateResultState(ctx, "res-1", domain.HiveCIProcessingStateVerified); err != nil {
		t.Fatalf("pending_result -> verified failed: %v", err)
	}

	mock.ExpectQuery("SELECT processing_state FROM hiveci_workflow_results").
		WithArgs("res-1").
		WillReturnRows(pgxmock.NewRows([]string{"processing_state"}).AddRow("verified"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").
		WithArgs("res-1", "processed").
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	if err := repo.UpdateResultState(ctx, "res-1", domain.HiveCIProcessingStateProcessed); err != nil {
		t.Fatalf("verified -> processed failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPgHiveCIRepository_InvalidResultStateTransition(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()

	repo := &PgHiveCIRepository{pool: mock}

	mock.ExpectQuery("SELECT processing_state FROM hiveci_workflow_results").
		WithArgs("res-2").
		WillReturnRows(pgxmock.NewRows([]string{"processing_state"}).AddRow("pending_run"))

	if err := repo.UpdateResultState(ctx, "res-2", domain.HiveCIProcessingStateProcessed); err == nil {
		t.Fatal("expected invalid transition error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPgHiveCIRepository_UpsertWorkflowResultRollsBackProjectionOnSecondWriteFailure(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()
	repo := &PgHiveCIRepository{pool: mock}
	result := domain.HiveCIWorkflowResult{ResultEventID: "res-rollback", RunEventID: "run-rollback", EventCreatedAt: time.Now().UTC()}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hiveci_workflow_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec("UPDATE hiveci_workflow_results r").WithArgs("run-rollback").WillReturnError(errors.New("projection update failed"))
	mock.ExpectRollback()

	if err := repo.UpsertWorkflowResult(ctx, result); err == nil {
		t.Fatal("UpsertWorkflowResult returned nil, want projection error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPgHiveCIRepository_UpsertIdempotency(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mock.Close()

	repo := &PgHiveCIRepository{pool: mock}
	now := time.Now().UTC()

	run := domain.HiveCIWorkflowRun{
		RunEventID:      "run-1",
		RepoCoordinate:  "repo",
		CommitSHA:       "abc",
		Branch:          "main",
		WorkflowPath:    ".github/workflows/ci.yml",
		PublisherPubkey: "pub",
		EventCreatedAt:  now,
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hiveci_workflow_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hiveci_workflow_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
	mock.ExpectCommit()
	if err := repo.UpsertWorkflowRun(ctx, run); err != nil {
		t.Fatalf("first run upsert: %v", err)
	}
	if err := repo.UpsertWorkflowRun(ctx, run); err != nil {
		t.Fatalf("second run upsert: %v", err)
	}

	result := domain.HiveCIWorkflowResult{
		ResultEventID:   "res-1",
		RunEventID:      "run-1",
		Status:          "success",
		ExitCode:        0,
		DurationSeconds: 10,
		PublisherPubkey: "pub",
		EventCreatedAt:  now,
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hiveci_workflow_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec("UPDATE hiveci_workflow_results r").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hiveci_workflow_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
	mock.ExpectExec("UPDATE hiveci_workflow_results r").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
	mock.ExpectCommit()
	if err := repo.UpsertWorkflowResult(ctx, result); err != nil {
		t.Fatalf("first result upsert: %v", err)
	}
	if err := repo.UpsertWorkflowResult(ctx, result); err != nil {
		t.Fatalf("second result upsert: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
