package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
	"github.com/stretchr/testify/require"
)

func TestNewExportsLiveGovernedSagaStore(t *testing.T) {
	defer stubDBHooks(t, errors.New("database unavailable"), nil)()
	signer := newFakeSoulFactorySigner(t)
	defer stubSoulFactoryHooks(t, signer, nil)()
	cfg := startupTestConfig(ModeFull)
	configureValidSoulFactory(t, cfg, signer.pubkey)
	app, err := New(cfg)
	require.NoError(t, err)
	defer app.Logger.Sync()
	defer closeRelayPools(app.relayPools...)
	defer app.soulFactoryCloser()
	defer app.Telemetry.Shutdown(context.Background())

	// Persist after startup: a scrape must observe the live engine's checkpoints,
	// not a separate metrics-only store or a startup snapshot.
	store, err := saga.NewFileStore(filepath.Join(cfg.SoulFactory.ProvisioningStateDir, "sagas"))
	require.NoError(t, err)
	run, err := saga.NewRun("request-live-metrics", "run-live-metrics", "agent-live-metrics", "spec-hash", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, store.Create(context.Background(), run))
	recorder := httptest.NewRecorder()
	app.HTTPServer.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "bahia_openclaw_provisioning_build_info{")
	require.Contains(t, recorder.Body.String(), `request_id="request-live-metrics"`)
}
