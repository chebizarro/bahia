package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func closeWithNoError(t *testing.T, closeFn func() error) {
	t.Helper()
	require.NoError(t, closeFn())
}

func syncTestLogger(t *testing.T, logger *zap.Logger) {
	t.Helper()
	if err := logger.Sync(); err != nil {
		t.Logf("test logger sync returned %v", err)
	}
}

// A reload candidate is built with New while the process controller already
// exists. One that changes nostr.outbound is rejected, so cmd/server keeps the
// running application instead of reporting settings it cannot apply.
func TestNewRejectsChangedOutboundAdmission(t *testing.T) {
	nostrout.Default()
	cfg := startupTestConfig("emergency")
	cfg.Nostr.Outbound.Lanes.Signer = config.NostrOutboundLaneConfig{RatePerMinute: 1, Burst: 1}
	_, err := New(cfg)
	require.ErrorIs(t, err, nostrout.ErrConfigFixed)
}

func TestNewStartsEmergencyModeWithoutDatabase(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("emergency")
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.DB)
	require.NotNil(t, app.Health)
	require.NotNil(t, app.Reconciler)
	checks := app.Health.Readiness().Checks
	var autoApplyWarning bool
	for _, check := range checks {
		if check.Name == "runtime_reconciliation" {
			autoApplyWarning = check.Status == HealthStatusWarn && check.Details["auto_apply"] == "suspended"
		}
	}
	require.True(t, autoApplyWarning, "operators must see the auto_apply safety hold")
}

func TestNewWiresDBLessRouteAndManagedSupervision(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	cfg := startupTestConfig("emergency")
	cfg.Supervision.Enabled = true
	cfg.RouteCanaries.Enabled = true
	cfg.EdgeRouting.Enabled = true
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	require.Nil(t, app.DB)
	require.NotNil(t, app.ManagedInstanceSupervisor)
	require.True(t, appHasRunner(app, "managed-instance-supervisor"))
	require.True(t, appHasRunner(app, "route-canary-supervisor"))
}

func TestNewKeepsFullModeWhenDatabaseAvailable(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, nil, nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("full")
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.NotNil(t, app.Health)
}

func TestNewDoesNotRegisterSoulFactoryWhenDisabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signetFactoryCalls := 0
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, &fakeSoulFactorySigner{}, func(cfg soulfactory.RuntimeAdapterConfig) {
		t.Fatalf("OpenClaw adapter factory called while SoulFactory disabled: %+v", cfg)
	})
	previousSignetFactory := newSoulFactorySignetClient
	newSoulFactorySignetClient = func(cfg signetAdapter.Config, logger *slog.Logger) (soulFactorySignerClient, error) {
		signetFactoryCalls++
		return previousSignetFactory(cfg, logger)
	}
	defer func() {
		newSoulFactorySignetClient = previousSignetFactory
		restoreSoulFactoryHooks()
	}()

	cfg := startupTestConfig("emergency")
	cfg.SoulFactory.Enabled = false
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.SoulFactory)
	require.False(t, appHasRunner(app, "soulfactory"))
	require.Zero(t, signetFactoryCalls)
}

func TestNewRegistersSoulFactoryWhenEnabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signer := newFakeSoulFactorySigner(t)
	var adapterConfigs []soulfactory.RuntimeAdapterConfig
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, func(cfg soulfactory.RuntimeAdapterConfig) {
		adapterConfigs = append(adapterConfigs, cfg)
	})
	defer restoreSoulFactoryHooks()

	cfg := startupTestConfig("full")
	configureValidSoulFactory(t, cfg, signer.pubkey)
	cfg.Nostr.BrowserRelays = []string{"wss://browser.example", "wss://relay.example"}
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	defer closeWithNoError(t, app.soulFactoryCloser)

	require.NotNil(t, app.SoulFactory)
	require.True(t, appHasRunner(app, "soulfactory"))
	require.False(t, signer.connected, "app.New must not synchronously connect Signet")
	require.Zero(t, signer.connectCalls, "app.New must not start a stale Signet attempt")
	require.True(t, appHasRunner(app, "signet-soulfactory"))
	recorder := httptest.NewRecorder()
	app.HTTPServer.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, adapterConfigs, 1)
	adapterConfig := adapterConfigs[0]
	require.Equal(t, domain.RuntimeTargetOpenClaw, adapterConfig.Target)
	require.Equal(t, signer.pubkey, adapterConfig.ControllerPubkey)
	require.Equal(t, []string{"wss://relay.example", "wss://private.example", "wss://browser.example"}, adapterConfig.Relays)
	require.Same(t, signer, adapterConfig.Signer)
}

func TestNewWiresBahiaIntegrationIntoSoulFactory(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signer := newFakeSoulFactorySigner(t)
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, nil)
	defer restoreSoulFactoryHooks()
	previousFactory := newSoulFactoryBahiaIntegration
	var wiredRegistry *service.RegistryService
	newSoulFactoryBahiaIntegration = func(registry *service.RegistryService, cfg soulfactory.BahiaIntegrationConfig, logger *slog.Logger) (*soulfactory.BahiaIntegration, error) {
		wiredRegistry = registry
		return previousFactory(registry, cfg, logger)
	}
	defer func() { newSoulFactoryBahiaIntegration = previousFactory }()

	cfg := startupTestConfig("full")
	configureValidSoulFactory(t, cfg, signer.pubkey)
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	defer closeWithNoError(t, app.soulFactoryCloser)
	require.Same(t, app.Registry, wiredRegistry)
}

func TestNewFailsClosedWhenBahiaIntegrationCannotStart(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signer := newFakeSoulFactorySigner(t)
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, nil)
	defer restoreSoulFactoryHooks()
	previousFactory := newSoulFactoryBahiaIntegration
	newSoulFactoryBahiaIntegration = func(*service.RegistryService, soulfactory.BahiaIntegrationConfig, *slog.Logger) (*soulfactory.BahiaIntegration, error) {
		return nil, errors.New("Bahia registry unavailable")
	}
	defer func() { newSoulFactoryBahiaIntegration = previousFactory }()

	cfg := startupTestConfig("full")
	configureValidSoulFactory(t, cfg, signer.pubkey)
	app, err := New(cfg)
	require.Nil(t, app)
	require.ErrorContains(t, err, "configuring SoulFactory Bahia integration")
	require.True(t, signer.closed, "failed startup must close the SoulFactory signer")
}

func TestNewRegistersMultipleSoulFactoryRuntimes(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signer := newFakeSoulFactorySigner(t)
	var adapterConfigs []soulfactory.RuntimeAdapterConfig
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, func(cfg soulfactory.RuntimeAdapterConfig) {
		adapterConfigs = append(adapterConfigs, cfg)
	})
	defer restoreSoulFactoryHooks()

	cfg := startupTestConfig("full")
	configureValidSoulFactory(t, cfg, signer.pubkey)
	cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "metiq", "synthetic-3"}
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	defer closeWithNoError(t, app.soulFactoryCloser)

	require.NotNil(t, app.SoulFactory)
	require.Len(t, adapterConfigs, 3)
	wantTargets := []domain.RuntimeTarget{
		domain.RuntimeTargetOpenClaw,
		domain.RuntimeTargetMetiq,
		domain.RuntimeTarget("synthetic-3"),
	}
	for i, want := range wantTargets {
		require.Equal(t, want, adapterConfigs[i].Target)
		require.Equal(t, signer.pubkey, adapterConfigs[i].ControllerPubkey)
		require.Same(t, signer, adapterConfigs[i].Signer)
	}
}

func TestNewFailsStartupOnDuplicateAgentRuntime(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	signer := newFakeSoulFactorySigner(t)
	factoryCalled := false
	restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, func(cfg soulfactory.RuntimeAdapterConfig) {
		factoryCalled = true
	})
	defer restoreSoulFactoryHooks()

	cfg := startupTestConfig("full")
	configureValidSoulFactory(t, cfg, signer.pubkey)
	cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "openclaw"}
	_, err := New(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "soul_factory.agent_runtimes")
	require.False(t, factoryCalled, "invalid agent_runtimes must fail validation before adapter construction")
}

func TestNewRejectsInvalidSoulFactoryConfig(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	factoryCalled := false
	previousSignetFactory := newSoulFactorySignetClient
	newSoulFactorySignetClient = func(cfg signetAdapter.Config, logger *slog.Logger) (soulFactorySignerClient, error) {
		factoryCalled = true
		return previousSignetFactory(cfg, logger)
	}
	defer func() { newSoulFactorySignetClient = previousSignetFactory }()

	cfg := startupTestConfig("full")
	cfg.SoulFactory.Enabled = true
	_, err := New(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "soul_factory.relays requires at least one relay")
	require.False(t, factoryCalled)
}

func TestNewRegistersDatabaseRecoveryRunnerWhenHigherTierStartupLosesDB(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("full")
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.True(t, appHasRunner(app, "database-recovery"))
}

func TestNewRegistersDatabaseRecoveryRunnerEvenInEmergencyMode(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("emergency")
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	// With tier gates deleted, the daemon always attempts DB recovery.
	require.True(t, appHasRunner(app, "database-recovery"))
}

func TestStartBackgroundRunnersReportsRestartRequest(t *testing.T) {
	manager := NewBackgroundManager(zap.NewNop())
	manager.RegisterWithOptions(&restartRequestRunner{}, RunnerRequired(false))

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startBackgroundRunners(ctx, manager, errCh)

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, errBackgroundRestartRequired)
	case <-time.After(2 * time.Second):
		require.Fail(t, "restart request was not reported")
	}
	manager.Wait()
}

func TestDatabaseRecoveryRunnerCompletesWithoutRestartAfterRecovery(t *testing.T) {
	t.Helper()

	var attempts atomic.Int32
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	origConnect := dbConnect
	origMigrate := dbMigrate
	dbConnect = func(ctx context.Context, cfg config.DBConfig, logger *zap.Logger) (*pgxpool.Pool, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("database unavailable")
		}
		return nil, nil
	}
	dbMigrate = func(context.Context, *pgxpool.Pool, *zap.Logger) error { return nil }
	defer func() {
		dbConnect = origConnect
		dbMigrate = origMigrate
		restoreDBHooks()
	}()

	runner := newDatabaseRecoveryRunner(config.DBConfig{}, 5*time.Millisecond, zap.NewNop())
	err := runner.Run(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
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

func appHasRunner(app *App, name string) bool {
	if app == nil || app.Background == nil {
		return false
	}
	for _, status := range app.Background.RunnerStatuses() {
		if status.Name == name {
			return true
		}
	}
	return false
}

func configureValidSoulFactory(t *testing.T, cfg *config.Config, controllerPubkey string) {
	t.Helper()
	cfg.SoulFactory = config.SoulFactoryConfig{
		Enabled:              true,
		Relays:               []string{"wss://relay.example"},
		AdditionalRelays:     []string{"wss://private.example", "wss://relay.example"},
		AuthorizedPubkeys:    []string{controllerPubkey},
		SoulFactoryPubkey:    controllerPubkey,
		SignetBunkerURI:      "bunker://" + controllerPubkey + "?relay=wss://relay.example",
		LLMBaseURL:           "https://llm.example",
		LLMModel:             "soul-model",
		LLMAPIKey:            "test-api-key",
		LLMTimeout:           30 * time.Second,
		ProvisioningStateDir: t.TempDir(),
	}
}

func stubSoulFactoryHooks(t *testing.T, signer *fakeSoulFactorySigner, captureAdapterConfig func(soulfactory.RuntimeAdapterConfig)) func() {
	t.Helper()
	previousSignetFactory := newSoulFactorySignetClient
	previousAdapterFactory := newSoulFactoryRuntimeAdapter
	previousGeneratorFactory := newSoulFactorySoulGenerator
	newSoulFactorySignetClient = func(cfg signetAdapter.Config, logger *slog.Logger) (soulFactorySignerClient, error) {
		if cfg.AllowMock {
			t.Fatal("SoulFactory app wiring must not enable Signet mock mode")
		}
		return signer, nil
	}
	newSoulFactoryRuntimeAdapter = func(cfg soulfactory.RuntimeAdapterConfig) (soulfactory.RuntimeAdapter, error) {
		if captureAdapterConfig != nil {
			captureAdapterConfig(cfg)
		}
		return fakeSoulFactoryRuntimeAdapter{target: cfg.Target}, nil
	}
	newSoulFactorySoulGenerator = func(cfg llm.Config, logger *slog.Logger) soulfactory.SoulGenerator {
		return fakeSoulFactoryGenerator{}
	}
	return func() {
		newSoulFactorySignetClient = previousSignetFactory
		newSoulFactoryRuntimeAdapter = previousAdapterFactory
		newSoulFactorySoulGenerator = previousGeneratorFactory
	}
}

func startupTestConfig(mode string) *config.Config {
	cfg := config.Defaults()
	cfg.Mode = mode
	cfg.Nostr.PrivateKey = nostr.Generate().Hex()
	cfg.Nostr.Relays = nil
	cfg.Loom.Relays = nil
	cfg.Reconcile.Enabled = true
	cfg.Server.Port = 0
	cfg.Nostr.LocalStore.Path = testLocalStorePath()
	return cfg
}

type fakeSoulFactorySigner struct {
	secret       string
	pubkey       string
	connected    bool
	closed       bool
	connectCtx   context.Context
	connectCalls int
}

func newFakeSoulFactorySigner(t *testing.T) *fakeSoulFactorySigner {
	t.Helper()
	secret := nostr.Generate()
	return &fakeSoulFactorySigner{secret: secret.Hex(), pubkey: secret.Public().Hex()}
}

func (s *fakeSoulFactorySigner) Connect(ctx context.Context) error {
	s.connectCalls++
	s.connected = true
	s.connectCtx = ctx
	return nil
}

func (s *fakeSoulFactorySigner) Ping(context.Context) error {
	if !s.connected {
		return signetAdapter.ErrNotConnected
	}
	return nil
}

func (s *fakeSoulFactorySigner) IsConnected() bool { return s.connected }

func (s *fakeSoulFactorySigner) WaitUntilConnected(ctx context.Context) error {
	for !s.connected {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return nil
}

func (s *fakeSoulFactorySigner) WaitUntilDisconnected(ctx context.Context) error {
	for s.connected {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return nil
}

func (s *fakeSoulFactorySigner) ConfiguredPublicKey() (string, error) { return s.pubkey, nil }

func (s *fakeSoulFactorySigner) GetPublicKey(context.Context) (string, error) {
	return s.pubkey, nil
}

func (s *fakeSoulFactorySigner) Close() error {
	s.closed = true
	return nil
}

func (s *fakeSoulFactorySigner) Sign(_ context.Context, event *nostr.Event) error {
	secret, err := nostr.SecretKeyFromHex(s.secret)
	if err != nil {
		return err
	}
	return event.Sign(secret)
}

func (s *fakeSoulFactorySigner) ProvisionAgent(context.Context, string, []int) (string, string, string, error) {
	pubkey, err := nostr.PubKeyFromHex(s.pubkey)
	if err != nil {
		return "", "", "", err
	}
	npub := nip19.EncodeNpub(pubkey)
	return s.pubkey, npub, "bunker://" + s.pubkey, nil
}

func (s *fakeSoulFactorySigner) RevokeAgent(context.Context, string) error  { return nil }
func (s *fakeSoulFactorySigner) SuspendAgent(context.Context, string) error { return nil }
func (s *fakeSoulFactorySigner) ResumeAgent(context.Context, string) error  { return nil }

type fakeSoulFactoryGenerator struct{}

func (fakeSoulFactoryGenerator) Generate(context.Context, domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	return &domain.SoulGeneratorOutput{SoulMD: "# Soul", IdentityMD: "# Identity", AllowedKinds: []int{0, 1}}, nil
}

type fakeSoulFactoryRuntimeAdapter struct {
	target domain.RuntimeTarget
}

func (a fakeSoulFactoryRuntimeAdapter) Runtime() domain.RuntimeTarget {
	if a.target == "" {
		return domain.RuntimeTargetOpenClaw
	}
	return a.target
}
func (fakeSoulFactoryRuntimeAdapter) DiscoverCapabilities(context.Context, domain.SoulRelayPolicySpec) ([]soulfactory.RuntimeCapability, error) {
	return nil, nil
}
func (fakeSoulFactoryRuntimeAdapter) Execute(context.Context, soulfactory.RuntimeAdapterRequest) (*soulfactory.RuntimeControlResultEnvelope, error) {
	return nil, nil
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

type restartRequestRunner struct{}

func (r *restartRequestRunner) Name() string { return "restart-request" }
func (r *restartRequestRunner) Run(context.Context) error {
	return errBackgroundRestartRequired
}
