package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/nostrmigration"
	"github.com/stretchr/testify/require"
)

// The bootstrap tier model is gone: RequireRepo middleware gates
// nil-repository routes with 503.

func TestRequireRepoGatesNilDependencies(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// nil dep → 503
	recorder := httptest.NewRecorder()
	middleware.RequireRepo(nil)(handler).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	// non-nil dep → passes through
	recorder = httptest.NewRecorder()
	middleware.RequireRepo("present")(handler).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestNewWithoutDatabaseBootsSuccessfully(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	app, err := New(startupTestConfig("full"))
	require.NoError(t, err, "the daemon must boot without Postgres")
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.DB)
	require.NotNil(t, app.Health)

	// The bootstrapper must still be registered.
	findBootstrapperRunner(t, app)
}

func findBootstrapperRunner(t *testing.T, app *App) *bootstrapperRunner {
	t.Helper()
	require.NotNil(t, app.Background)
	app.Background.mu.Lock()
	defer app.Background.mu.Unlock()
	for _, reg := range app.Background.runners {
		if runner, ok := reg.runner.(*bootstrapperRunner); ok {
			return runner
		}
	}
	t.Fatal("relay bootstrapper runner is not registered")
	return nil
}

// The nostr_events migration (cmd/bahia-migrate) re-signs and republishes
// events, so it runs offline, never on the daemon startup path.
func TestNewDoesNotRegisterNostrMigrationOnStartup(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	app, err := New(startupTestConfig("full"))
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	findBootstrapperRunner(t, app)
	app.Background.mu.Lock()
	defer app.Background.mu.Unlock()
	for _, reg := range app.Background.runners {
		_, isMigration := reg.runner.(*nostrmigration.Runner)
		require.False(t, isMigration, "nostrmigration.Runner must not be registered on startup")
		require.NotEqual(t, "nostr-migration", reg.runner.Name())
	}
}
