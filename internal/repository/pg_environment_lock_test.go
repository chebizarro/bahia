package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgEnvironmentRepositoryGetByIDForUpdateLocksRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	envID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM environments WHERE id = \\$1 FOR UPDATE").
		WithArgs(envID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "name", "loom_worker_selector", "runtime_config", "targeting",
			"deploy_strategy", "protected", "created_at", "updated_at",
		}).AddRow(
			envID, uuid.Nil, "prod", []byte(`{}`), []byte(`{}`), []byte(`{"default_unit_key":"max"}`),
			"replace", false, now, now,
		))

	repo := newPgEnvironmentRepositoryWithDB(mock)
	env, err := repo.GetByIDForUpdate(context.Background(), envID)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.Equal(t, envID, env.ID)
	require.Equal(t, "max", env.Targeting.DefaultUnitKey)
	require.True(t, env.UpdatedAt.Equal(now))
	require.NoError(t, mock.ExpectationsWereMet())
}
