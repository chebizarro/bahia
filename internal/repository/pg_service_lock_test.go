package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgServiceRepositoryGetByIDForUpdateLocksRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	serviceID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM services WHERE id = \\$1 FOR UPDATE").
		WithArgs(serviceID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "name", "repo_url", "repository", "artifact_repo", "default_branch",
			"runtime_type", "runtime_config", "created_at", "updated_at",
		}).AddRow(
			serviceID, uuid.Nil, "api", "https://git.example/api", []byte(`{}`), "registry.example/api", "main",
			"docker", []byte(`{}`), now, now,
		))

	repo := newPgServiceRepositoryWithDB(mock)
	svc, err := repo.GetByIDForUpdate(context.Background(), serviceID)
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.Equal(t, serviceID, svc.ID)
	require.True(t, svc.UpdatedAt.Equal(now))
	require.NoError(t, mock.ExpectationsWereMet())
}
