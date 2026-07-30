package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgDeploymentUnitRepositoryCreateAndGet(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgDeploymentUnitRepositoryWithDB(mock)
	envID := uuid.New()
	unitID := uuid.New()
	runtimeConfig := map[string]any{"compose_dir": "/srv/bahia/compose/prod", "endpoint_ref": "prod-docker", "network_profile": map[string]any{"zone": "a"}}
	networkProfile := map[string]string{"zone": "a"}
	unit := &domain.DeploymentUnit{
		ID:             unitID,
		EnvironmentID:  envID,
		Key:            "default",
		DisplayName:    "Default",
		RuntimeType:    domain.RuntimeTypeCompose,
		NetworkProfile: networkProfile,
		GitSource:      &domain.GitSourceBinding{RepositoryURL: "https://git.example/bahia.git", Branch: "main", CommitSHA: "abc123"},
		ReconcileMode:  domain.ReconcileModeObserveOnly,
		OwnershipMode:  domain.OwnershipModeBahiaManaged,
		RuntimeConfig:  runtimeConfig,
	}

	mock.ExpectExec("INSERT INTO deployment_units").
		WithArgs(unitID, envID, "default", "Default", domain.RuntimeTypeCompose, "prod-docker", "/srv/bahia/compose/prod", "", pgxmock.AnyArg(), domain.ReconcileModeObserveOnly, domain.OwnershipModeBahiaManaged, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.Create(context.Background(), unit))
	require.False(t, unit.Implicit)

	configJSON, err := json.Marshal(runtimeConfig)
	require.NoError(t, err)
	now := time.Now().UTC()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + deploymentUnitColumns + " FROM deployment_units WHERE id = $1")).
		WithArgs(unitID).
		WillReturnRows(pgxmock.NewRows(splitColumns(deploymentUnitColumns)).AddRow(
			unitID, envID, "default", "Default", domain.RuntimeTypeCompose, "prod-docker", "/srv/bahia/compose/prod", "", []byte(`{"zone":"a"}`), domain.ReconcileModeObserveOnly,
			domain.OwnershipModeBahiaManaged, configJSON, now, now,
		))

	got, err := repo.GetByID(context.Background(), unitID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, envID, got.EnvironmentID)
	require.Equal(t, domain.RuntimeTypeCompose, got.RuntimeType)
	require.Equal(t, "prod-docker", got.EndpointRef)
	require.Equal(t, "/srv/bahia/compose/prod", got.ComposeDir)
	require.Equal(t, "a", got.NetworkProfile["zone"])
	require.NotNil(t, got.GitSource)
	require.Equal(t, "https://git.example/bahia.git", got.GitSource.RepositoryURL)
	require.Equal(t, "main", got.GitSource.Branch)
	require.Equal(t, "abc123", got.GitSource.CommitSHA)
	require.Equal(t, "/srv/bahia/compose/prod", got.RuntimeConfig["compose_dir"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDeploymentUnitRepositoryResolveDefaultSynthesizesWhenMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgDeploymentUnitRepositoryWithDB(mock)
	env := &domain.Environment{
		ID:            uuid.New(),
		RuntimeConfig: map[string]any{"type": "podman", "podman_host": "unix:///run/podman/podman.sock"},
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+deploymentUnitColumns+" FROM deployment_units\n\t\tWHERE environment_id = $1 AND unit_key = $2")).
		WithArgs(env.ID, domain.DefaultDeploymentUnitKey).
		WillReturnRows(pgxmock.NewRows(splitColumns(deploymentUnitColumns)))

	unit, err := repo.ResolveDefault(context.Background(), env)
	require.NoError(t, err)
	require.NotNil(t, unit)
	require.True(t, unit.Implicit)
	require.Equal(t, uuid.Nil, unit.ID)
	require.Equal(t, domain.RuntimeTypePodman, unit.RuntimeType)
	require.Equal(t, domain.ReconcileModeObserveOnly, unit.ReconcileMode)
	require.NoError(t, mock.ExpectationsWereMet())
}
