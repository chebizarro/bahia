// Package app wires together all components and manages the application lifecycle.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/harbor"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	registryAdapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/reconcile"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/workflow"
	"go.uber.org/zap"
)

// App holds all application components.
type App struct {
	Config      *config.Config
	Logger      *zap.Logger
	DB          *pgxpool.Pool
	Registry    *service.RegistryService
	HTTPServer  *http.Server
	Publisher   events.Publisher
	Coordinator *workflow.Coordinator
	Reconciler  *reconcile.Reconciler
	NostrPub    *nostrAdapter.Publisher
	Telemetry   *telemetry.Provider
	Background  *BackgroundManager
}

// New creates and wires together all application components.
func New(cfg *config.Config) (*App, error) {
	// Logger.
	var logger *zap.Logger
	var err error
	if cfg.Log.Format == "console" {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}

	ctx := context.Background()

	// Database.
	pool, err := db.Connect(ctx, cfg.DB, logger)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	// Run migrations.
	if err := db.Migrate(ctx, pool, logger); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// Repositories.
	serviceRepo := repository.NewPgServiceRepository(pool)
	envRepo := repository.NewPgEnvironmentRepository(pool)
	buildRepo := repository.NewPgBuildRepository(pool)
	artifactRepo := repository.NewPgArtifactRepository(pool)
	intentRepo := repository.NewPgDeploymentIntentRepository(pool)
	runRepo := repository.NewPgDeploymentRunRepository(pool)
	obsRepo := repository.NewPgRuntimeObservationRepository(pool)
	stateRepo := repository.NewPgEnvironmentServiceStateRepository(pool)

	// Nostr event audit repository.
	nostrEventRepo := repository.NewPgNostrEventRepository(pool)

	// Worker catalog repository.
	workerRepo := repository.NewPgWorkerRepository(pool)

	// Payment repository.
	paymentRepo := repository.NewPgPaymentRecordRepository(pool)

	// SBOM repository.
	sbomRepo := repository.NewPgSBOMRepository(pool)

	// Signature and policy repositories.
	sigRepo := repository.NewPgArtifactSignatureRepository(pool)
	policyRepo := repository.NewPgDeploymentPolicyRepository(pool)

	// Secret repository.
	secretRepo := repository.NewPgSecretRepository(pool)

	// Event publisher.
	publisher := events.NewInProcessPublisher(logger)

	// Adapters.
	// Shared relay pool for Nostr and Loom connections.
	relayURLs := cfg.Nostr.Relays
	for _, r := range cfg.Loom.Relays {
		found := false
		for _, existing := range relayURLs {
			if r == existing {
				found = true
				break
			}
		}
		if !found {
			relayURLs = append(relayURLs, r)
		}
	}
	relayPool := nostrAdapter.NewRelayPool(relayURLs, logger)
	relayPool.Connect(ctx)

	loomClient := loom.NewClient(cfg.Loom, cfg.Nostr.PrivateKey, relayPool, logger,
		loom.WithWorkerRepo(workerRepo),
	)

	// Image verifier: use Harbor (legacy), or the new multi-registry adapter, or no-op.
	var verifier service.ImageVerifier
	switch {
	case cfg.Harbor.Enabled:
		harborClient := harbor.NewClient(cfg.Harbor, logger)
		verifier = harbor.NewVerifier(harborClient, logger)
		logger.Info("harbor image verification enabled", zap.String("url", cfg.Harbor.URL))
	case cfg.Registry.URL != "" || cfg.Registry.Type != "":
		v, err := registryAdapter.NewVerifier(registryAdapter.RegistryConfig{
			Type:     registryAdapter.RegistryType(cfg.Registry.Type),
			URL:      cfg.Registry.URL,
			Username: cfg.Registry.Username,
			Password: cfg.Registry.Password,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("creating registry verifier: %w", err)
		}
		verifier = v
		logger.Info("OCI registry verification enabled",
			zap.String("type", string(cfg.Registry.Type)),
			zap.String("url", cfg.Registry.URL))
	default:
		logger.Info("image verification disabled, artifacts will not be verified against registry")
	}

	// Registry service.
	registry := service.NewRegistryService(
		serviceRepo, envRepo, buildRepo, artifactRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, publisher, logger,
	)
	nostrPub := nostrAdapter.NewPublisher(cfg.Nostr, relayPool, nostrEventRepo, logger)
	nostrPub.SetupSubscriptions(publisher)

	// Worker policy service for environment-specific worker selection.
	workerPolicySvc := service.NewWorkerPolicyService(workerRepo, logger)

	// Workflow coordinator.
	coord := workflow.NewCoordinator(registry, loomClient, publisher, logger,
		workflow.WithWorkerPolicy(workerPolicySvc),
	)
	coord.SetupEventHandlers(publisher)

	// Runtime observer — uses the factory to select Docker, Compose, or Kubernetes.
	observer, err := runtime.NewObserver(runtime.RuntimeConfig{
		Type:          cfg.Runtime.Type,
		DockerHost:    cfg.Runtime.DockerHost,
		ComposeDir:    cfg.Runtime.ComposeDir,
		KubeContext:   cfg.Runtime.KubeContext,
		KubeNamespace: cfg.Runtime.KubeNamespace,
		KubeConfig:    cfg.Runtime.KubeConfig,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("creating runtime observer: %w", err)
	}
	logger.Info("runtime observer initialized", zap.String("type", cfg.Runtime.Type))

	// Reconciler (created here but started in Run() with the lifecycle context).
	var rec *reconcile.Reconciler
	if cfg.Reconcile.Enabled {
		rec = reconcile.NewReconciler(
			serviceRepo, envRepo, artifactRepo, obsRepo, stateRepo,
			observer, publisher, cfg.Reconcile.Interval, logger,
		)
	}

	// Telemetry.
	telemetryProvider := telemetry.Setup(telemetry.Config{
		Enabled:     true,
		ServiceName: "bahia",
	}, logger)

	// Background runner manager.
	bgManager := NewBackgroundManager(logger)

	// Register the reconciler as a background runner (if enabled).
	if rec != nil {
		bgManager.Register(&reconcilerRunner{rec: rec})
	}

	// Nostr event processor: maps inbound events to domain commands.
	nostrProcessor := nostrAdapter.NewProcessor(registry, workerRepo, logger)

	// Nostr inbound subscriber: listens for Hive-CI, Loom, and Bahia events.
	nostrSub := nostrAdapter.NewSubscriber(relayPool, nostrEventRepo, logger,
		nostrAdapter.WithHandler(nostrProcessor.Handle),
	)
	bgManager.Register(nostrSub)

	// Payment service (Cashu integration).
	var paymentSvc *service.PaymentService
	if cfg.Cashu.Enabled {
		paymentSvc = service.NewPaymentService(paymentRepo, workerRepo, runRepo, logger)
		logger.Info("cashu payment integration enabled", zap.String("mint_url", cfg.Cashu.MintURL))
	}

	// Policy service.
	policySvc := service.NewPolicyService(policyRepo, sigRepo, sbomRepo, logger)

	// Notification system.
	notifRepo := repository.NewPgNotificationRepository(pool)
	notifDispatcher := notifications.NewDispatcher(notifRepo, logger)
	notifDispatcher.RegisterSender(domain.ChannelTypeWebhook, notifications.NewWebhookSender())
	if cfg.Nostr.PrivateKey != "" {
		notifDispatcher.RegisterSender(domain.ChannelTypeNostrDM,
			notifications.NewNostrDMSender(relayPool, cfg.Nostr.PrivateKey, logger))
	}
	notifDispatcher.SetupSubscriptions(publisher)

	// Real-time event stream hub.
	eventHub := handlers.NewEventStreamHub(publisher, logger)

	// Secret encryptor (uses Bahia's Nostr key for at-rest encryption).
	var secretEncryptor *secretsAdapter.Encryptor
	if cfg.Nostr.PrivateKey != "" {
		secretEncryptor = secretsAdapter.NewEncryptor(cfg.Nostr.PrivateKey)
		logger.Info("secrets encryption enabled")
	}

	// HTTP router.
	handler := router.NewWithDeps(registry, logger, cfg.CORS, telemetryProvider,
		router.RouterDeps{
			Workers:   workerRepo,
			Payments:  paymentSvc,
			SBOMs:     sbomRepo,
			Artifacts: artifactRepo,
			EventHub:  eventHub,
			Policies:  policySvc,
			Secrets:       secretRepo,
			Encryptor:     secretEncryptor,
			Notifications: notifRepo,
			Dispatcher:    notifDispatcher,
		}, cfg.Auth)

	httpServer := &http.Server{
		Addr:         cfg.ServerAddress(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	return &App{
		Config:      cfg,
		Logger:      logger,
		DB:          pool,
		Registry:    registry,
		HTTPServer:  httpServer,
		Publisher:   publisher,
		Coordinator: coord,
		Reconciler:  rec,
		NostrPub:    nostrPub,
		Telemetry:   telemetryProvider,
		Background:  bgManager,
	}, nil
}

// Run starts the HTTP server and blocks until shutdown.
func (a *App) Run() error {
	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start all registered background runners.
	a.Background.Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		a.Logger.Info("HTTP server starting", zap.String("addr", a.HTTPServer.Addr))
		if err := a.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		a.Logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.Server.ShutdownTimeout)
	defer cancel()

	// Shut down the HTTP server first (stop accepting new requests).
	if err := a.HTTPServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	// Shut down the workflow coordinator (cancel in-flight polls, wait for completion).
	a.Coordinator.Shutdown(a.Config.Server.ShutdownTimeout)

	// Wait for background runners to finish (they should stop when ctx is cancelled).
	a.Background.Wait()

	// Shutdown telemetry.
	if a.Telemetry != nil {
		_ = a.Telemetry.Shutdown(shutdownCtx)
	}

	// Close Nostr relay connections.
	if a.NostrPub != nil {
		a.NostrPub.Close()
	}

	a.DB.Close()
	_ = a.Logger.Sync()
	a.Logger.Info("server stopped gracefully")
	return nil
}

// reconcilerRunner adapts the reconcile.Reconciler to the BackgroundRunner interface.
type reconcilerRunner struct {
	rec *reconcile.Reconciler
}

func (r *reconcilerRunner) Name() string { return "reconciler" }
func (r *reconcilerRunner) Run(ctx context.Context) error {
	r.rec.Run(ctx)
	return nil
}
