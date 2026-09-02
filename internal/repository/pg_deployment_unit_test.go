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

func TestPgDeploymentUnitRepositoryUpdateAndProtectedDelete(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgDeploymentUnitRepositoryWithDB(mock)
	envID := uuid.New()
	unitID := uuid.New()
	unit := &domain.DeploymentUnit{
		ID:            unitID,
		EnvironmentID: envID,
		Key:           "max",
		RuntimeType:   domain.RuntimeTypeCompose,
		EndpointRef:   "max",
		ComposeDir:    "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		RuntimeConfig: map[string]any{"execution_mode": "sdk"},
	}

	mock.ExpectExec("UPDATE deployment_units").
		WithArgs(unitID, "max", "", domain.RuntimeTypeCompose, "max", "/srv/bahia/gastown", "", pgxmock.AnyArg(), domain.ReconcileModeAutoApply, domain.OwnershipModeBahiaManaged, pgxmock.AnyArg(), pgxmock.AnyArg(), envID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.Update(context.Background(), unit))

	mock.ExpectExec("DELETE FROM deployment_units").
		WithArgs(unitID).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(unitID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	err = repo.DeleteIfUnreferenced(context.Background(), unitID)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDeploymentUnitRepositoryListForUpdateLocksRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgDeploymentUnitRepositoryWithDB(mock)
	envID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM deployment_units.+FOR UPDATE").
		WithArgs(envID).
		WillReturnRows(pgxmock.NewRows(splitColumns(deploymentUnitColumns)))

	units, err := repo.ListByEnvironmentForUpdate(context.Background(), envID)
	require.NoError(t, err)
	require.Empty(t, units)
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

	mock.ExpectQuery("SELECT .+ FROM deployment_units.+WHERE environment_id = \\$1").
		WithArgs(env.ID).
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

func TestPgDeploymentUnitRepositoryResolveDefaultUsesConfiguredExplicitKey(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgDeploymentUnitRepositoryWithDB(mock)
	env := &domain.Environment{
		ID:        uuid.New(),
		Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "max"},
	}
	unitID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM deployment_units.+WHERE environment_id = \\$1").
		WithArgs(env.ID).
		WillReturnRows(pgxmock.NewRows(splitColumns(deploymentUnitColumns)).AddRow(
			unitID, env.ID, "max", "Max", domain.RuntimeTypeCompose, "max", "/srv/bahia/gastown", "",
			[]byte(`{}`), domain.ReconcileModeAutoApply, domain.OwnershipModeBahiaManaged, []byte(`{"execution_mode":"sdk"}`), now, now,
		))

	unit, err := repo.ResolveDefault(context.Background(), env)
	require.NoError(t, err)
	require.NotNil(t, unit)
	require.Equal(t, unitID, unit.ID)
	require.Equal(t, "max", unit.Key)
	require.False(t, unit.Implicit)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDeploymentUnitRepositoryResolveDefaultFailsClosedForMissingConfiguredKey(t *testing.T) {
	tests := []struct {
		name      string
		targetKey string
		withUnit  bool
	}{
		{name: "non-default key with no explicit units", targetKey: "max"},
		{name: "configured key missing from explicit set", targetKey: "max", withUnit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			repo := newPgDeploymentUnitRepositoryWithDB(mock)
			env := &domain.Environment{ID: uuid.New(), Targeting: domain.EnvironmentTargeting{DefaultUnitKey: test.targetKey}}
			rows := pgxmock.NewRows(splitColumns(deploymentUnitColumns))
			if test.withUnit {
				now := time.Now().UTC()
				rows.AddRow(uuid.New(), env.ID, "default", "Default", domain.RuntimeTypeDocker, "", "", "",
					[]byte(`{}`), domain.ReconcileModeObserveOnly, domain.OwnershipModeBahiaManaged, []byte(`{}`), now, now)
			}
			mock.ExpectQuery("SELECT .+ FROM deployment_units.+WHERE environment_id = \\$1").
				WithArgs(env.ID).
				WillReturnRows(rows)

			unit, err := repo.ResolveDefault(context.Background(), env)
			require.Nil(t, unit)
			require.ErrorIs(t, err, ErrConflict)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
