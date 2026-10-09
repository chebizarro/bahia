package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseLossLeavesRecoveryOutsideCoreReadiness(t *testing.T) {
	restore := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restore()

	app, err := New(startupTestConfig("full"))
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	require.Nil(t, app.DB)
	requireCheckStatus(t, app.Health.Readiness().Checks, "postgres_index", HealthStatusWarn)

	found := false
	for _, status := range app.Background.RunnerStatuses() {
		if status.Name != "database-recovery" {
			continue
		}
		found = true
		require.False(t, status.Required, "optional PostgreSQL recovery must not gate readiness")
	}
	require.True(t, found, "database recovery runner must remain observable")

	manager := NewBackgroundManager(app.Logger)
	manager.RegisterWithOptions(newDatabaseRecoveryRunner(app.Config.DB, 0, app.Logger), RunnerRequired(false))
	provider := NewHealthProvider(nil, manager)
	requireCheckStatus(t, provider.Readiness().Checks, "background_runners", HealthStatusPass)
}
