package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewImplicitDefaultDeploymentUnitUsesEnvironmentRuntimeConfig(t *testing.T) {
	envID := uuid.New()
	env := &Environment{
		ID: envID,
		RuntimeConfig: map[string]any{
			"type":        "compose",
			"compose_dir": "/srv/bahia/compose/prod",
		},
	}

	unit, err := NewImplicitDefaultDeploymentUnit(env)
	require.NoError(t, err)
	require.True(t, unit.Implicit)
	require.Equal(t, uuid.Nil, unit.ID)
	require.Equal(t, envID, unit.EnvironmentID)
	require.Equal(t, DefaultDeploymentUnitKey, unit.Key)
	require.Equal(t, RuntimeTypeCompose, unit.RuntimeType)
	require.Equal(t, ReconcileModeObserveOnly, unit.ReconcileMode)
	require.Equal(t, OwnershipModeBahiaManaged, unit.OwnershipMode)
	require.Equal(t, "/srv/bahia/compose/prod", unit.RuntimeConfig["compose_dir"])
}

func TestValidateDeploymentUnitRequiresPolicyAndOwnership(t *testing.T) {
	unit := &DeploymentUnit{
		EnvironmentID: uuid.New(),
		Key:           DefaultDeploymentUnitKey,
		RuntimeType:   RuntimeTypeDocker,
		ReconcileMode: ReconcileModeObserveOnly,
		OwnershipMode: OwnershipModeBahiaManaged,
	}
	require.NoError(t, ValidateDeploymentUnit(unit))

	unit.ReconcileMode = ""
	require.Error(t, ValidateDeploymentUnit(unit))
	unit.ReconcileMode = ReconcileModeObserveOnly
	unit.OwnershipMode = ""
	require.Error(t, ValidateDeploymentUnit(unit))
}
