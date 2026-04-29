package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
)

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

	mock.ExpectExec("INSERT INTO hiveci_workflow_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
	mock.ExpectExec("INSERT INTO hiveci_workflow_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
	mock.ExpectExec("UPDATE hiveci_workflow_results").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
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

	mock.ExpectExec("INSERT INTO hiveci_workflow_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec("UPDATE hiveci_workflow_results r").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mock.ExpectExec("INSERT INTO hiveci_workflow_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
	mock.ExpectExec("UPDATE hiveci_workflow_results r").WithArgs("run-1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
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
