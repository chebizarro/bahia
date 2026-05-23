package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewStartsEmergencyModeWithoutDatabase(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig(ModeEmergency)
	app, err := New(cfg)
	require.NoError(t, err)
	defer app.Logger.Sync()
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.DB)
	require.NotNil(t, app.ModePolicy)
	require.Equal(t, Tier1, app.ModePolicy.ActiveTier)
	require.NotNil(t, app.Health)
}

func TestNewKeepsFullModeWhenDatabaseAvailable(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, nil, nil)
	defer restoreDBHooks()

	cfg := startupTestConfig(ModeFull)
	app, err := New(cfg)
	require.NoError(t, err)
	defer app.Logger.Sync()
	defer closeRelayPools(app.relayPools...)

	require.NotNil(t, app.ModePolicy)
	require.Equal(t, Tier3, app.ModePolicy.ActiveTier)
	require.Equal(t, Tier3, app.ModePolicy.RequestedTier)
	require.NotNil(t, app.Health)
}

func TestStartBackgroundRunnersRespectsActiveTier(t *testing.T) {
	manager := NewBackgroundManager(zap.NewNop())
	policy := NewModePolicy(ModeFull)
	policy.SetActiveTier(Tier1)

	tier0 := newGatedRunner("tier0")
	tier1 := newGatedRunner("tier1")
	tier2 := newGatedRunner("tier2")
	manager.RegisterWithOptions(tier0, RunnerTier(Tier0))
	manager.RegisterWithOptions(tier1, RunnerTier(Tier1))
	manager.RegisterWithOptions(tier2, RunnerTier(Tier2))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startBackgroundRunners(ctx, manager, policy)

	requireRunnerStarted(t, tier0)
	requireRunnerStarted(t, tier1)
	select {
	case <-tier2.started:
		require.Fail(t, "tier2 runner started despite active tier1")
	default:
	}

	cancel()
	manager.Wait()
}

func stubDBHooks(t *testing.T, connectErr error, migrateErr error) func() {
	t.Helper()
	origConnect := dbConnect
	origMigrate := dbMigrate
	dbConnect = func(context.Context, config.DBConfig, *zap.Logger) (*pgxpool.Pool, error) {
		if connectErr != nil {
			return nil, connectErr
		}
		return nil, nil
	}
	dbMigrate = func(context.Context, *pgxpool.Pool, *zap.Logger) error {
		return migrateErr
	}
	return func() {
		dbConnect = origConnect
		dbMigrate = origMigrate
	}
}

func startupTestConfig(mode Mode) *config.Config {
	cfg := config.Defaults()
	cfg.Mode = string(mode)
	cfg.Nostr.PrivateKey = nostr.GeneratePrivateKey()
	cfg.Nostr.Relays = nil
	cfg.Nostr.EncryptedRequestRelays = nil
	cfg.Loom.Relays = nil
	cfg.Reconcile.Enabled = true
	cfg.Server.Port = 0
	return cfg
}

type gatedRunner struct {
	name    string
	started chan struct{}
}

func newGatedRunner(name string) *gatedRunner {
	return &gatedRunner{name: name, started: make(chan struct{})}
}

func (r *gatedRunner) Name() string { return r.name }
func (r *gatedRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	return nil
}

func requireRunnerStarted(t *testing.T, runner *gatedRunner) {
	t.Helper()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		require.Failf(t, "runner did not start", "%s did not start", runner.name)
	}
}
