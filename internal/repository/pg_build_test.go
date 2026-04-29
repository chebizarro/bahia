package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgBuildRepository_GetByCISystemRunID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgBuildRepository{pool: mock}
	now := time.Now().UTC()
	startedAt := now
	finishedAt := now
	id := uuid.New()
	svcID := uuid.New()

	mock.ExpectQuery("FROM builds WHERE ci_system = \\$1 AND ci_run_id = \\$2").
		WithArgs("hive-ci", "run-123").
		WillReturnRows(pgxmock.NewRows([]string{"id", "service_id", "git_sha", "git_ref", "ci_system", "ci_run_id", "loom_job_id", "status", "source_event_id", "started_at", "finished_at", "metadata", "created_at"}).
			AddRow(id, svcID, "abc123", "main", "hive-ci", "run-123", "", "succeeded", "evt-1", &startedAt, &finishedAt, []byte(`{"k":"v"}`), now))

	build, err := repo.GetByCISystemRunID(context.Background(), "hive-ci", "run-123")
	require.NoError(t, err)
	require.NotNil(t, build)
	require.Equal(t, id, build.ID)
	require.Equal(t, "run-123", build.CIRunID)
	require.Equal(t, "v", build.Metadata["k"])

	require.NoError(t, mock.ExpectationsWereMet())
}
