package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// B-10 (bahia-irsry.3): without Postgres every tier2/tier3 repository is nil,
// so nothing may raise the active tier (and so un-gate tier3 routes) above
// the tier whose dependencies were actually constructed.

func TestNewWithoutDatabaseCapsBootstrapperRequestedTier(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	app, err := New(startupTestConfig(ModeFull))
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	runner := findBootstrapperRunner(t, app)
	require.Equal(t, Tier3, app.ModePolicy.RequestedTier)
	require.Equal(t, Tier1, app.ModePolicy.ActiveTier())
	progress := runner.bootstrapper.Progress()
	require.Equal(t, int(Tier1), progress.RequestedTier,
		"bootstrapper must not request (and so report) a tier above the database-less dependency cap")
}

func TestBootstrapReadyTierCannotRaiseActiveTierOverNilRepositories(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	policy := NewModePolicy(ModeFull)
	pool, available := connectOptionalDatabase(context.Background(), startupTestConfig(ModeFull), zap.NewNop(), policy)
	require.Nil(t, pool)
	require.False(t, available)
	require.Equal(t, Tier1, policy.ActiveTier())

	// bootstrapperRunner.Run applies the bootstrapper's ready tier this way.
	// With the production catalog every tier3 replay group is optional, so a
	// bootstrapper asked for tier 3 reports 3 once the tier0/1 groups finish.
	policy.SetActiveTier(Tier3)

	require.Equal(t, Tier1, policy.ActiveTier())
	require.False(t, policy.RouteEnabled(Tier2))
	require.False(t, policy.RouteEnabled(Tier3))

	nilRepoHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("tier3 handler backed by nil repositories was reached")
	})
	recorder := httptest.NewRecorder()
	middleware.TierGate(policy, int(Tier3))(nilRepoHandler).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func findBootstrapperRunner(t *testing.T, app *App) *bootstrapperRunner {
	t.Helper()
	require.NotNil(t, app.Background)
	app.Background.mu.Lock()
	defer app.Background.mu.Unlock()
	for _, reg := range app.Background.runners {
		ordered, ok := reg.runner.(*orderedStartupRunner)
		if !ok {
			continue
		}
		for _, inner := range ordered.runners {
			if runner, ok := inner.(*bootstrapperRunner); ok {
				return runner
			}
		}
	}
	t.Fatal("relay bootstrapper runner is not registered")
	return nil
}
