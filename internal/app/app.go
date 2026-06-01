// Package app wires together all components and manages the application lifecycle.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nbd-wtf/go-nostr"
	backupAdapter "github.com/openagentsinc/bahia/internal/adapters/backup"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	dnsAdapter "github.com/openagentsinc/bahia/internal/adapters/dns"
	"github.com/openagentsinc/bahia/internal/adapters/harbor"
	hiveciAdapter "github.com/openagentsinc/bahia/internal/adapters/hiveci"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	registryAdapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/adapters/signing"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	packagefactory "github.com/openagentsinc/bahia/internal/backends/factory"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/pipeline"
	"github.com/openagentsinc/bahia/internal/reconcile"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory"
	"github.com/openagentsinc/bahia/internal/workflow"
	"go.uber.org/zap"
)

// App holds all application components.
type App struct {
	Config             *config.Config
	Logger             *zap.Logger
	DB                 *pgxpool.Pool
	Registry           *service.RegistryService
	MLRegistry         *service.MLRegistryService
	LLMRegistry        *service.LLMRegistryService
	HTTPServer         *http.Server
	Publisher          events.Publisher
	Coordinator        *workflow.Coordinator
	Reconciler         *reconcile.Reconciler
	NostrPub           *nostrAdapter.Publisher
	Telemetry          *telemetry.Provider
	Background         *BackgroundManager
	toolCoordinator    *service.ToolProvisioningCoordinator
	relayPools         []*nostrAdapter.RelayPool
	ModePolicy         *ModePolicy
	Health             *HealthProvider
	RelayFirstRegistry *service.RelayFirstRegistry
}

var (
	dbConnect = db.Connect
	dbMigrate = db.Migrate
)

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
	policy := NewModePolicy(configuredMode(cfg.Mode))
	pressureThresholds := workerPressureThresholds(cfg.WorkerPressure)

	// Event publisher and tier0/tier1 continuity stores are available before the
	// disposable PostgreSQL projection cache is attempted.
	publisher := events.NewInProcessPublisher(logger)
	continuityDefinitionStore := service.NewInMemoryContinuityDefinitionStore()
	continuityHeartbeatMonitor := service.NewInMemoryHeartbeatMonitor()
	continuityStatusStore := service.NewInMemoryContinuityStatusStore()
	continuityRecipeExecutor := service.NewContinuityRecipeExecutor(publisher, service.WithContinuityRecipeLogger(logger))

	// Relay pools are initialized before the optional database cache.
	controlPlaneRelays := controlPlaneRelayURLs(cfg.Nostr)
	controlPlanePool := nostrAdapter.NewRelayPool(controlPlaneRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
	controlPlanePool.Connect(ctx)

	relayURLs := interopRelayURLs(cfg, controlPlaneRelays)
	relayPool := nostrAdapter.NewRelayPool(relayURLs, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
	relayPool.Connect(ctx)
	logger.Info("nostr relay topology initialized",
		zap.Strings("control_plane_relays", controlPlaneRelays),
		zap.Strings("interop_relays", relayURLs),
		zap.Bool("sidecar_enabled", cfg.Nostr.Sidecar.Enabled),
		zap.Bool("mirror_external", cfg.Nostr.Sidecar.MirrorExternal),
	)

	// Database cache is optional. When unavailable, keep tier0/tier1 relay-first
	// startup alive and use in-memory event audit/cursor storage.
	pool, dbAvailable := connectOptionalDatabase(ctx, cfg, logger, policy)

	// Repositories. When DB is unavailable, PG-backed repositories are nil.
	// Route gating prevents tier2/tier3 routes from being accessed, so nil repos
	// won't be hit on those paths. Tier1 uses in-memory stores exclusively.
	var serviceRepo repository.ServiceRepository
	var envRepo repository.EnvironmentRepository
	var buildRepo repository.BuildRepository
	var artifactRepo repository.ArtifactRepository
	var intentRepo repository.DeploymentIntentRepository
	var runRepo repository.DeploymentRunRepository
	var obsRepo repository.RuntimeObservationRepository
	var stateRepo repository.EnvironmentServiceStateRepository
	var toolProvisionRepo repository.ToolProvisioningRepository
	var workerRepo repository.WorkerRepository
	var paymentRepo repository.PaymentRecordRepository
	var sbomRepo repository.SBOMRepository
	var sigRepo repository.ArtifactSignatureRepository
	var policyRepo repository.DeploymentPolicyRepository
	var secretRepo repository.SecretRepository
	var deploymentUnitRepo repository.DeploymentUnitRepository
	var orgRepo repository.OrganizationRepository
	var orgMemberRepo repository.OrgMemberRepository
	var orgInviteRepo repository.OrgInviteRepository

	if dbAvailable {
		serviceRepo = repository.NewPgServiceRepository(pool)
		envRepo = repository.NewPgEnvironmentRepository(pool)
		buildRepo = repository.NewPgBuildRepository(pool)
		artifactRepo = repository.NewPgArtifactRepository(pool)
		intentRepo = repository.NewPgDeploymentIntentRepository(pool)
		runRepo = repository.NewPgDeploymentRunRepository(pool)
		obsRepo = repository.NewPgRuntimeObservationRepository(pool)
		stateRepo = repository.NewPgEnvironmentServiceStateRepository(pool)
		toolProvisionRepo = repository.NewPgToolProvisioningRepository(pool)
		workerRepo = repository.NewPgWorkerRepository(pool)
		paymentRepo = repository.NewPgPaymentRecordRepository(pool)
		sbomRepo = repository.NewPgSBOMRepository(pool)
		sigRepo = repository.NewPgArtifactSignatureRepository(pool)
		policyRepo = repository.NewPgDeploymentPolicyRepository(pool)
		secretRepo = repository.NewPgSecretRepository(pool)
		deploymentUnitRepo = repository.NewPgDeploymentUnitRepository(pool)
		orgRepo = repository.NewPgOrganizationRepository(pool)
		orgMemberRepo = repository.NewPgOrgMemberRepository(pool)
		orgInviteRepo = repository.NewPgOrgInviteRepository(pool)
	} else {
		logger.Warn("database unavailable: tier2/tier3 repositories are nil, route gating will return 503 for those tiers")
	}

	// Nostr event audit repository.
	var nostrEventRepo repository.NostrEventRepository
	if dbAvailable {
		nostrEventRepo = repository.NewPgNostrEventRepository(pool)
	} else {
		nostrEventRepo = repository.NewInMemoryNostrEventRepository()
	}

	loomClient := loom.NewClient(cfg.Loom, cfg.Nostr.PrivateKey, relayPool, logger,
		loom.WithWorkerRepo(workerRepo),
	)

	// Image verifier: use Harbor (legacy), or the new multi-registry adapter, or no-op.
	var verifier service.ImageVerifier
	var signVerifier mcp.SignatureVerifier
	var pipelineRegistryInspector registryAdapter.ImageInspector
	switch {
	case cfg.Harbor.Enabled:
		harborClient := harbor.NewClient(cfg.Harbor, logger)
		verifier = harbor.NewVerifier(harborClient, logger)
		inspector, err := registryAdapter.NewInspector(registryAdapter.RegistryConfig{
			Type:     registryAdapter.RegistryHarbor,
			URL:      cfg.Harbor.URL,
			Username: cfg.Harbor.Username,
			Password: cfg.Harbor.Password,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("creating harbor pipeline inspector: %w", err)
		}
		pipelineRegistryInspector = inspector
		logger.Info("harbor image verification enabled", zap.String("url", cfg.Harbor.URL))
	case cfg.Registry.URL != "" || cfg.Registry.Type != "":
		inspector, err := registryAdapter.NewInspector(registryAdapter.RegistryConfig{
			Type:     registryAdapter.RegistryType(cfg.Registry.Type),
			URL:      cfg.Registry.URL,
			Username: cfg.Registry.Username,
			Password: cfg.Registry.Password,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("creating registry verifier: %w", err)
		}
		verifier = &registryAdapter.VerifierAdapter{Inspector: inspector}
		pipelineRegistryInspector = inspector
		signVerifier = signing.NewCosignVerifier(inspector, logger)
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

	// Relay-first write path: when mode is not "full" OR when explicitly enabled,
	// wrap registry mutations so relay publish must succeed before local DB writes.
	// In full mode, this defaults off for backward compatibility with existing
	// DB-first semantics. Set mode to degraded/emergency or configure
	// relay_canonical_writes: true to activate.
	var relayFirstRegistry *service.RelayFirstRegistry
	if policy.RequestedMode != ModeFull || cfg.Nostr.PublishEnabled {
		signer := service.RelayFirstPrivateKeySigner(cfg.Nostr.PrivateKey)
		relayFirstRegistry = service.NewRelayFirstRegistry(registry, relayFirstNostrPublisher{pool: relayPool}, signer, logger)
		logger.Info("relay-first write path enabled for core registry mutations",
			zap.String("mode", string(policy.RequestedMode)))
	}
	controlPlaneSigner, err := controlplane.NewPrivateKeySigner(cfg.Nostr.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("configuring control-plane signer: %w", err)
	}
	var continuityProjectionPublisher service.ContinuityNostrPublishFunc
	if controlPlanePool != nil && controlPlaneSigner != nil {
		continuityProjectionPublisher = func(ctx context.Context, kind int, tags nostr.Tags, content string) error {
			ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: content}
			if err := controlplane.SignGoNostrEvent(ctx, controlPlaneSigner, ev); err != nil {
				return fmt.Errorf("sign continuity projection event: %w", err)
			}
			published, err := controlPlanePool.Publish(ctx, *ev)
			if err != nil {
				return fmt.Errorf("publish continuity projection event: %w", err)
			}
			if published == 0 {
				return fmt.Errorf("publish continuity projection event: no relay accepted the request")
			}
			return nil
		}
	}
	service.NewContinuityStatusProjector(publisher, continuityStatusStore, continuityProjectionPublisher, logger)

	pressureMonitor := service.NewWorkerPressureMonitor()
	workerStatePublisher := controlplane.NewWorkerStatePublisher(controlPlanePool, controlPlaneSigner)
	workerStatePublisher.ConfigureAudit(nostrEventRepo, logger)

	// Worker policy service for environment-specific worker selection.
	workerPolicySvc := service.NewWorkerPolicyService(workerRepo, logger, service.WithWorkerPolicyPressureThresholds(pressureThresholds))

	// Runtime resolver — selects Docker, Compose, or Kubernetes per service/environment.
	runtimeRegistryAuth := runtimeRegistryAuth(cfg)
	runtimeResolver := runtime.NewConfigRuntimeResolver(cfg.Runtime, logger, runtimeRegistryAuth)
	logger.Info("runtime resolver initialized", zap.String("default_type", cfg.Runtime.Type))

	// Workflow coordinator.
	coord := workflow.NewCoordinator(registry, loomClient, publisher, logger,
		workflow.WithWorkerPolicy(workerPolicySvc),
		workflow.WithRuntimeResolver(runtimeResolver),
	)
	coord.SetupEventHandlers(publisher)

	// Secret encryptor (uses Bahia's Nostr key for at-rest encryption).
	var secretEncryptor *secretsAdapter.Encryptor
	if cfg.Nostr.PrivateKey != "" {
		secretEncryptor, err = secretsAdapter.NewEncryptor(cfg.Nostr.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("configuring secret encryption: %w", err)
		}
		logger.Info("secrets encryption enabled")
	}

	// Adopted workload orchestration and direct runtime lifecycle services.
	// Privileged routes are opt-in; keep services nil unless their route family is enabled.
	var adoptionSvc *service.AdoptionService
	if cfg.Adoption.Enabled {
		adoptionSvc = service.NewAdoptionService(
			registry, serviceRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, publisher, logger,
			service.WithAdoptionRuntimeConfig(cfg.Runtime, cfg.Adoption.AllowRawDockerHosts),
			service.WithAdoptionComposeTakeoverPolicy(cfg.Adoption.AllowComposeTakeover),
			service.WithAdoptionSecrets(secretRepo, secretEncryptor),
			service.WithAdoptionOrganizations(orgRepo),
			service.WithAdoptionRuntimeIdentities(repository.NewPgAdoptedRuntimeIdentityRepository(pool)),
			service.WithAdoptionTxExecutor(repository.NewPgTxExecutor(pool)),
		)
	}
	var runtimeLifecycleSvc *service.RuntimeLifecycleService
	if cfg.DirectRuntime.Enabled {
		var runtimeApplyLockOpts []service.RuntimeLifecycleOption
		runtimeApplyLockOpts = append(runtimeApplyLockOpts, service.WithRuntimeLifecycleSecrets(secretRepo, secretEncryptor))
		if dbAvailable {
			runtimeApplyLockOpts = append(runtimeApplyLockOpts, service.WithRuntimeApplyLock(service.NewRuntimeApplyLock(pool, logger)))
		}
		runtimeLifecycleSvc = service.NewRuntimeLifecycleService(
			registry, serviceRepo, envRepo, artifactRepo, stateRepo, runtimeResolver, publisher, logger,
			runtimeApplyLockOpts...,
		)
	}

	// Reconciler (created here but started in Run() with the lifecycle context).
	var rec *reconcile.Reconciler
	if cfg.Reconcile.Enabled {
		reconcilerOpts := []reconcile.Option{}
		if runtimeLifecycleSvc != nil {
			reconcilerOpts = append(reconcilerOpts, reconcile.WithAutoRemediationDeployer(runtimeLifecycleSvc))
		}
		rec = reconcile.NewReconciler(
			serviceRepo, envRepo, artifactRepo, deploymentUnitRepo, obsRepo, stateRepo,
			runtimeResolver, publisher, cfg.Reconcile.Interval, logger,
			reconcilerOpts...,
		)
	}

	// Telemetry.
	telemetryProvider := telemetry.Setup(telemetry.Config{
		Enabled:      cfg.Telemetry.Enabled,
		ServiceName:  cfg.Telemetry.ServiceName,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
	}, logger)

	// Background runner manager and startup health provider.
	bgManager := NewBackgroundManager(logger)
	healthProvider := NewHealthProvider(policy, bgManager)
	healthProvider.SetRelayQuorumConfig(RelayQuorumConfig{
		FullMinHealthy:      cfg.Nostr.RelayQuorum.FullMinHealthy,
		DegradedMinHealthy:  cfg.Nostr.RelayQuorum.DegradedMinHealthy,
		EmergencyMinHealthy: cfg.Nostr.RelayQuorum.EmergencyMinHealthy,
	})
	healthProvider.SetRelayHealthFunc(func() (connected, healthy int) {
		return aggregateRelayHealth(controlPlanePool, relayPool)
	})

	catalog := nostrAdapter.NewKindCatalog()
	cursorPlanner := nostrAdapter.NewReplayCursorPlanner(time.Second, nostrAdapter.NewNostrEventRepositoryCursorSource(nostrEventRepo))

	// Relay projection cache: applies decoded relay events to local repositories.
	// When DB is unavailable, appliers are skipped (tier1-only mode has no
	// projection cache). Uses in-memory meta repo so bootstrap can track
	// ordering without requiring Postgres.
	var bootstrapCache nostrAdapter.BootstrapCacheApplier
	if dbAvailable {
		projectionMetaRepo := newInMemoryProjectionMetaRepo()
		projectionCache := service.NewRelayProjectionCache(projectionMetaRepo, logger)
		projectionCache.RegisterTier1Tier2Appliers(service.ProjectionCacheRepositories{
			Workers:      workerRepo,
			Services:     serviceRepo,
			Environments: envRepo,
			Builds:       buildRepo,
			Artifacts:    artifactRepo,
			Policies:     policyRepo,
		})
		bootstrapCache = &bootstrapCacheAdapter{cache: projectionCache}
	}

	// Bahia self-identity publisher: emits 31410/31411/30360 events to relays.
	// Wired but gated behind nostrPub availability (requires relay connectivity).
	var bahiaStatusProjector *service.BahiaStatusProjector
	if nostrPub != nil {
		bahiaStatusProjector = service.NewBahiaStatusProjector(nostrPub, logger, cfg.Nostr.PrivateKey)
	}

	servicePubkey := ""
	if strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
		servicePubkey, _ = nostr.GetPublicKey(cfg.Nostr.PrivateKey)
	}
	bootstrapper := nostrAdapter.NewBootstrapper(relayPool, catalog, cursorPlanner, bootstrapCache, logger, nostrAdapter.BootstrapConfig{
		RequestedTier:       int(policy.RequestedTier),
		ProjectionAuthors:   compactBootstrapAuthors([]string{servicePubkey}),
		ControlPlaneAuthors: compactBootstrapAuthors([]string{servicePubkey}, cfg.Nostr.AuthorizedPubkeys, cfg.Auth.BootstrapOwnerPubkeys),
	})
	healthProvider.SetBootstrapFunc(func() (phase string, ready bool) {
		progress := bootstrapper.Progress()
		return string(progress.Phase), bootstrapper.Ready()
	})
	bgManager.RegisterWithOptions(&bootstrapperRunner{
		bootstrapper:    bootstrapper,
		policy:          policy,
		statusProjector: bahiaStatusProjector,
		catalogVersion:  catalog.Version,
	}, RunnerTier(Tier0))

	continuityGraph, err := service.NewContinuityGraph(
		continuityDefinitionStore,
		continuityHeartbeatMonitor,
		continuityStatusStore,
		continuityWorkerReader{repo: workerRepo, logger: logger},
	)
	if err != nil {
		return nil, fmt.Errorf("creating continuity graph: %w", err)
	}
	continuityFailoverTrigger, err := service.NewFailoverTriggerEngine(
		continuityHeartbeatMonitor,
		continuityDefinitionStore,
		publisher,
		time.Minute,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("creating continuity failover trigger engine: %w", err)
	}
	setupContinuityRuntimeSubscriptions(
		publisher,
		continuityDefinitionStore,
		continuityHeartbeatMonitor,
		continuityRecipeExecutor,
		continuityFailoverTrigger,
		logger,
	)
	bgManager.RegisterWithOptions(&failoverTriggerRunner{engine: continuityFailoverTrigger}, RunnerTier(Tier1))

	var fipsRelayPool *nostrAdapter.RelayPool
	if cfg.FIPS.Enabled {
		fipsRelayPool = nostrAdapter.NewRelayPool(cfg.FIPS.RelayURLs, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
		fipsRelayPool.Connect(ctx)
		fipsSubscriber := nostrAdapter.NewFIPSSubscriber(fipsRelayPool, workerRepo, logger,
			nostrAdapter.WithFIPSAppNamespace(cfg.FIPS.AppNamespace),
			nostrAdapter.WithFIPSAutoRegisterWorkers(cfg.FIPS.AutoRegisterWorkers),
			nostrAdapter.WithFIPSAllowedNpubs(cfg.FIPS.AllowedNpubs),
			nostrAdapter.WithFIPSWorkerUpdateHandler(func(_ context.Context, worker *domain.Worker, advert nostrAdapter.OverlayAdvert) {
				if worker == nil {
					return
				}
				logger.Info("FIPS overlay advert received",
					zap.String("worker_pubkey", worker.PubKey),
					zap.String("worker_name", worker.Name),
					zap.String("overlay_addr", worker.FIPSOverlayAddr),
					zap.Int("advert_version", advert.Version),
					zap.Int("transport_endpoints", len(advert.Endpoints)),
					zap.Strings("signal_relays", advert.SignalRelays),
				)
			}),
		)
		bgManager.RegisterWithOptions(&fipsSubscriberRunner{subscriber: fipsSubscriber}, RunnerTier(Tier3))
		logger.Info("FIPS overlay advert subscriber registered", zap.Strings("relay_urls", cfg.FIPS.RelayURLs), zap.String("app_namespace", cfg.FIPS.AppNamespace))
	}

	backupRegistryRepo := repository.NewPgBackupControlPlaneRepository(pool)
	backupRegistry := service.NewBackupRegistryService(backupRegistryRepo, publisher, logger)
	backupResponder := controlplane.NewBackupRunResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupRestoreResponder := controlplane.NewBackupRestoreResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupRetentionResponder := controlplane.NewBackupRetentionResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupResolver, err := service.NewStaticBackupBackendResolver(backupAdapter.NewKopiaBackend(), backupAdapter.NewVeleroBackend())
	if err != nil {
		return nil, fmt.Errorf("configuring backup backend resolver: %w", err)
	}
	backupCoordinator := service.NewBackupRunCoordinator(backupRegistry, backupResolver, logger, service.WithBackupRunResponder(backupResponder))
	backupRestoreCoordinator := service.NewBackupRestoreCoordinator(backupRegistry, backupResolver, logger, service.WithBackupRestoreResponder(backupRestoreResponder))
	backupRetentionCoordinator := service.NewBackupRetentionCoordinator(backupRegistry, backupResolver, logger, service.WithBackupRetentionResponder(backupRetentionResponder))
	bgManager.RegisterWithOptions(backupCoordinator, RunnerTier(Tier3))
	bgManager.RegisterWithOptions(backupRestoreCoordinator, RunnerTier(Tier3))
	bgManager.RegisterWithOptions(backupRetentionCoordinator, RunnerTier(Tier3))
	logger.Info("backup control plane registered", zap.String("backend", string(domain.BackupBackendKopia)))

	// Generic AI/ML registry foundation. Bucket-B keeps this additive and keeps
	// long-running orchestration on the existing LLM path until dedicated buckets.
	mlRegistryRepo := repository.NewPgMLRegistryRepository(pool)
	mlRegistry := service.NewMLRegistryService(mlRegistryRepo, publisher, logger, service.WithMLEnvironmentRepository(envRepo))
	workerReadModelSvc := service.NewWorkerReadModelService(workerRepo, registry, mlRegistry, workerPolicySvc, service.NewMLPlacementService(workerRepo, logger, service.WithMLPlacementPressureThresholds(pressureThresholds)), logger)
	workerCleanupOrchestrator := service.NewWorkerCleanupOrchestrator(workerRepo, workerReadModelSvc, loomCleanupClient{client: loomClient}, publisher, service.WorkerCleanupConfig{Mode: cfg.WorkerCleanup.Mode, Cooldown: cfg.WorkerCleanup.Cooldown, TargetFreeGB: cfg.WorkerCleanup.TargetFreeGB, PaymentToken: cfg.WorkerCleanup.PaymentToken, RequiredSoftware: cfg.WorkerCleanup.RequiredSoftware, PressureThresholds: pressureThresholds}, logger)
	setupWorkerPressureSubscriptions(publisher, pressureMonitor, workerStatePublisher, workerCleanupOrchestrator, workerRepo, logger)

	// LLM provisioning control plane.
	var llmRegistry *service.LLMRegistryService
	var llmResponder *controlplane.LLMResponder
	if cfg.LLM.Enabled {
		llmRouteRepo := repository.NewPgLLMRouteRepository(pool)
		llmReleaseRepo := repository.NewPgLLMReleaseRepository(pool)
		llmIntentRepo := repository.NewPgLLMDeploymentIntentRepository(pool)
		llmRunRepo := repository.NewPgLLMDeploymentRunRepository(pool)
		llmObsRepo := repository.NewPgLLMRouteObservationRepository(pool)
		llmStateRepo := repository.NewPgLLMRouteStateRepository(pool)
		llmRegistry = service.NewLLMRegistryService(llmRouteRepo, llmReleaseRepo, envRepo, llmIntentRepo, llmRunRepo, llmObsRepo, llmStateRepo, publisher, logger)

		gatewayManager := llmadapter.NewHTTPGatewayRouteManager(llmGatewayHTTPConfig(cfg.LLM), nil)
		provisioners := llmadapter.StaticProvisionerResolver{}
		externalProvisioner := llmadapter.NewExternalAPIProvisioner(nil)
		provisioners[domain.LLMBackendKindExternalAPI] = externalProvisioner
		for _, kind := range []domain.LLMBackendKind{domain.LLMBackendKindVLLM, domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP} {
			p, err := llmadapter.NewRuntimeProvisioner(kind, cfg.Runtime, logger)
			if err != nil {
				return nil, fmt.Errorf("creating LLM runtime provisioner %s: %w", kind, err)
			}
			provisioners[kind] = p
		}
		placementSvc := service.NewLLMPlacementService(workerRepo, logger, service.WithLLMPlacementPressureThresholds(pressureThresholds))
		coordOpts := []service.LLMProvisioningCoordinatorOption{
			service.WithLLMCoordinatorIntervals(cfg.LLM.CoordinatorPollInterval, cfg.LLM.StaleRunTimeout),
		}
		if controlPlaneSigner != nil && controlPlanePool != nil {
			llmResponder = controlplane.NewLLMResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo)
			coordOpts = append(coordOpts, service.WithLLMProvisioningResponder(llmResponder))
		}
		llmCoordinator := service.NewLLMProvisioningCoordinator(llmRegistry, envRepo, llmRunRepo, placementSvc, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, logger, coordOpts...)
		llmReconciler := reconcile.NewLLMRouteReconciler(llmRegistry, envRepo, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, cfg.LLM.ReconcileInterval, logger)
		bgManager.RegisterWithOptions(llmCoordinator, RunnerTier(Tier3))
		bgManager.RegisterWithOptions(llmReconciler, RunnerTier(Tier3))
		logger.Info("LLM control plane enabled", zap.String("default_gateway_ref", cfg.LLM.DefaultGatewayRef))
	}

	// Policy service.
	policySvc := service.NewPolicyService(policyRepo, sigRepo, sbomRepo, logger)

	// DNS persistence repositories are optional until concrete PostgreSQL adapters are available.
	var dnsZoneRepo repository.DNSZoneRepository
	var dnsPolicyRepo repository.DNSPolicyRepository
	var dnsRecordOverrideRepo repository.DNSRecordOverrideRepository

	var dnsProjector *reconcile.DNSProjector
	var dnsZones []domain.DNSZone
	var dnsResolver *dnsAdapter.StaticResolver
	var dnsOperator controlplane.DNSControlPlaneOperator
	if cfg.DNS.Enabled {
		dnsZones, dnsResolver, err = buildDNSRuntime(ctx, cfg.DNS, logger)
		if err != nil {
			return nil, err
		}
		dnsProjector = reconcile.NewDNSProjector(serviceRepo, envRepo, stateRepo, obsRepo, llmRegistry, mlRegistry, workerRepo, cfg.DNS, logger)
		dnsProjector.SetContinuityStatusReader(continuityDNSStatusReader{reader: continuityStatusStore})
		dnsReconciler := reconcile.NewDNSReconciler(dnsProjector, dnsZones, dnsResolverBridge{resolver: dnsResolver}, cfg.DNS.ReconcileInterval, logger)
		dnsReconciler.SetPublisher(publisher)
		if subscriber, ok := any(dnsReconciler).(interface{ SetupSubscriptions(events.Publisher) }); ok {
			subscriber.SetupSubscriptions(publisher)
		}
		var dnsPersistence controlplane.DNSPersistenceOperator
		if dnsZoneRepo != nil && dnsRecordOverrideRepo != nil {
			dnsPersistence = dnsRepositoryPersistenceAdapter{zones: dnsZoneRepo, overrides: dnsRecordOverrideRepo}
		}
		dnsOperator = newDNSControlPlaneOperator(dnsReconciler, dnsZones, dnsPersistence)
		bgManager.RegisterWithOptions(dnsReconciler, RunnerTier(Tier3))
		logger.Info("DNS orchestration enabled", zap.Int("zones", len(dnsZones)), zap.Strings("backends", dnsResolver.Refs()))
	}

	// Package repository control plane.
	var packageProjection repository.PackageControlPlaneRepository
	var packageRegistrySvc *service.PackageRegistryService
	if cfg.Packages.Enabled {
		packageProjection = repository.NewPgPackageControlPlaneRepository(pool)
		packageBackends, err := packagefactory.BuildRegistryWithSecrets(ctx, cfg.Packages, secretsAdapter.NewResolver(secretRepo, secretEncryptor))
		if err != nil {
			return nil, fmt.Errorf("building package backends: %w", err)
		}
		packageRegistrySvc, err = service.NewPackageRegistryService(cfg.Packages, packageBackends, packageProjection, nil, logger)
		if err != nil {
			return nil, fmt.Errorf("creating package registry service: %w", err)
		}
		logger.Info("package control plane enabled", zap.Int("backends", len(cfg.Packages.Backends)))
	}

	// Nostr read-model projector. This owns canonical 3196x projections and
	// the 310xx audit/activity feed for relay consumers; the legacy Publisher is
	// retained for relay pool lifecycle compatibility.
	projectorOpts := []nostrAdapter.ProjectorOption{
		nostrAdapter.WithPolicyProjectionSource(policySvc),
		nostrAdapter.WithBackupProjectionSource(backupRegistry),
		nostrAdapter.WithMLProjectionSource(mlRegistry),
		nostrAdapter.WithWorkerProjectionSource(workerRepo),
		nostrAdapter.WithWorkerReadModelProjectionSource(workerReadModelSvc),
		nostrAdapter.WithSystemDiscoveryConfig(cfg, true),
	}
	if llmRegistry != nil {
		projectorOpts = append(projectorOpts, nostrAdapter.WithLLMProjectionSource(llmRegistry))
	}
	if dnsProjector != nil {
		projectorOpts = append(projectorOpts,
			nostrAdapter.WithDNSProjectionSource(dnsProjector),
			nostrAdapter.WithDNSZoneProjectionSource(staticDNSZoneProjectionSource{zones: dnsZones}),
			nostrAdapter.WithDNSBackendProjectionSource(configDNSBackendProjectionSource{backends: cfg.DNS.Backends, zones: dnsZones, resolver: dnsResolver}),
		)
	}
	nostrProjector := nostrAdapter.NewProjector(cfg.Nostr, registry, controlPlanePool, nostrEventRepo, logger, projectorOpts...)
	nostrProjector.SetupSubscriptions(publisher)
	if nostrProjector.Enabled() {
		bgManager.RegisterWithOptions(nostrProjector, RunnerTier(Tier2))
		logger.Info("nostr read-model projector registered")
	}

	// Register the reconciler as a background runner (if enabled).
	if rec != nil {
		bgManager.RegisterWithOptions(&reconcilerRunner{rec: rec}, RunnerTier(Tier2))
	}

	// Nostr event processor: maps inbound events to domain commands.
	nostrProcessor := nostrAdapter.NewProcessorWithPublisher(registry, workerRepo, publisher, logger, nostrAdapter.WithPressureThresholds(pressureThresholds))

	// Blossom client wiring (used for artifact storage and browsing).
	var blossomClient *blossom.Client
	blossomCfg := blossom.Config{
		Servers:       cfg.Blossom.Servers,
		MaxRetries:    cfg.Blossom.MaxRetries,
		RetryDelay:    cfg.Blossom.RetryDelay,
		Timeout:       cfg.Blossom.Timeout,
		PrivateKeyHex: cfg.Blossom.PrivateKey,
	}
	if len(blossomCfg.Servers) == 0 && cfg.Blossom.URL != "" {
		blossomCfg.Servers = []string{cfg.Blossom.URL}
	}
	if len(blossomCfg.Servers) > 0 {
		blossomClient = blossom.NewClient(blossomCfg, slog.Default())
		logger.Info("blossom client enabled", zap.Strings("servers", blossomCfg.Servers))
	}
	var runLogService *runtime.LogService
	if blossomClient != nil {
		runLogService = runtime.NewLogService(blossomClient, nil, logger)
	}

	// OCI Registry wiring.
	var ociHandler http.Handler
	var ociRepo repository.OCIRegistryRepository
	if cfg.OCI.Enabled {
		pgOCIRepo := repository.NewPgOCIRepository(pool)
		ociRepo = pgOCIRepo
		if blossomClient == nil {
			return nil, fmt.Errorf("OCI registry requires Blossom servers to be configured")
		}
		ociSvc, err := service.NewOCIRegistryService(cfg.OCI, pgOCIRepo, pgOCIRepo, blossomClient, logger)
		if err != nil {
			return nil, fmt.Errorf("create oci registry service: %w", err)
		}
		nip98Validator := auth.NewNIP98Validator(auth.DefaultNIP98Config())
		ociHandler = handlers.NewOCIRegistryHandler(ociSvc, nip98Validator, cfg.OCI)
		bgManager.RegisterWithOptions(NewOCIUploadCleanupRunner(ociSvc, cfg.OCI.UploadExpiry, logger), RunnerTier(Tier3))
		logger.Info("oci registry enabled", zap.String("host", cfg.OCI.PublicHost))
	}

	// Hive-CI wiring.
	if cfg.HiveCI.Enabled {
		hiveRepo := repository.NewPgHiveCIRepository(pool)
		bridge := pipeline.NewBridge(hiveRepo, buildRepo, artifactRepo, intentRepo, envRepo, ociRepo, pipelineRegistryInspector, cfg.HiveCI.TrustedCIPubkeys, logger)
		// Wrap bridge.ProcessResult to match the ResultConsumer signature (no error return).
		onResult := func(ctx context.Context, resultEventID string) {
			if err := bridge.ProcessResult(ctx, resultEventID); err != nil {
				logger.Error("bridge process result failed", zap.String("result_event_id", resultEventID), zap.Error(err))
			}
		}
		hiveSub := hiveciAdapter.NewSubscriber(relayPool, hiveRepo, cfg.HiveCI.TrustedCIPubkeys, logger, onResult)
		bgManager.RegisterWithOptions(hiveSub, RunnerTier(Tier3))
		bgManager.RegisterWithOptions(NewHiveCIRetryRunner(hiveRepo, bridge, cfg.HiveCI.RetryInterval, cfg.HiveCI.MaxRetries, logger), RunnerTier(Tier3))
		logger.Info("hive-ci bridge enabled")
	}

	// Payment service exposes payment record/history and cost-estimate APIs.
	// It does not create or redeem Cashu tokens; cashu.enabled live wallet mode
	// remains fail-closed until mint-backed proof flows are implemented.
	paymentSvc := service.NewPaymentService(paymentRepo, workerRepo, runRepo, logger)
	if cfg.Cashu.Enabled {
		return nil, fmt.Errorf("cashu.enabled=true is unsupported because mint-backed token flows are not implemented; disable cashu.enabled")
	}

	// Notification system.
	notifRepo := repository.NewPgNotificationRepository(pool)
	notifDispatcher := notifications.NewDispatcher(notifRepo, logger)
	notifDispatcher.RegisterSender(domain.ChannelTypeWebhook, notifications.NewWebhookSender())
	if cfg.Nostr.PrivateKey != "" {
		notifDispatcher.RegisterSender(domain.ChannelTypeNostrDM,
			notifications.NewNostrDMSender(relayPool, cfg.Nostr.PrivateKey, logger))
	}
	notifDispatcher.SetupSubscriptions(publisher)

	// Tool provisioning orchestration.
	var toolCoordinator *service.ToolProvisioningCoordinator
	toolBuilder := build.NewDockerBuilder(cfg.Runtime.DockerHost, logger)
	toolSecurity := service.NewToolSecurityService(toolProvisionRepo, nil, logger, service.ToolSecurityConfig{})
	defaultRuntime, rtErr := runtime.NewRuntime(runtime.RuntimeConfig{Type: cfg.Runtime.Type, DockerHost: cfg.Runtime.DockerHost, ComposeDir: cfg.Runtime.ComposeDir, RegistryAuth: runtimeRegistryAuth, KubeContext: cfg.Runtime.KubeContext, KubeNamespace: cfg.Runtime.KubeNamespace, KubeConfig: cfg.Runtime.KubeConfig}, logger)
	if rtErr != nil {
		logger.Warn("default runtime init for tool provisioning failed", zap.Error(rtErr))
	}
	toolCoordinator = service.NewToolProvisioningCoordinator(
		toolProvisionRepo,
		serviceRepo,
		envRepo,
		toolSecurity,
		toolBuilder,
		defaultRuntime,
		controlplane.NewToolResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo),
		notifDispatcher,
		logger,
		service.ToolProvisioningConfig{BaseImageRef: "", TargetRegistry: cfg.Registry.URL, TargetRepo: "tools/swarmstr", InstallerVersion: "v1"},
	)
	// Explicit recovery for stranded stored intents; newly-arrived kind 5976
	// requests are still processed directly by the event-driven reactor path.
	bgManager.RegisterWithOptions(toolCoordinator, RunnerTier(Tier3))

	// MCP (Model Context Protocol) server for AI agent integration.
	var mlCommandPublisher mcp.MLCommandPublisher
	if mlRegistry != nil && controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		mlCommandPublisher = controlplane.NewMLCommandPublisher(controlPlanePool, controlPlaneSigner)
	}
	var llmCommandPublisher mcp.LLMCommandPublisher
	if llmRegistry != nil && controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		llmCommandPublisher = controlplane.NewLLMCommandPublisher(controlPlanePool, controlPlaneSigner)
	}
	var serviceCommandPublisher mcp.ServiceCommandPublisher
	if controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		serviceCommandPublisher = controlplane.NewServiceCommandPublisher(controlPlanePool, controlPlaneSigner)
	}
	var packageCommandPublisher mcp.PackageCommandPublisher
	if packageRegistrySvc != nil && controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		packageCommandPublisher = controlplane.NewPackageCommandPublisher(controlPlanePool, controlPlaneSigner)
	}
	var workerCommandPublisher mcp.WorkerCommandPublisher
	if controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		workerCommandPublisher = controlplane.NewWorkerCommandPublisher(controlPlanePool, controlPlaneSigner)
	}
	mcpDeps := mcp.ServerDeps{
		LogService:              runLogService,
		Payments:                paymentSvc,
		SBOMs:                   sbomRepo,
		Signatures:              sigRepo,
		SignVerifier:            signVerifier,
		MLRegistry:              mlRegistry,
		MLCommandPublisher:      mlCommandPublisher,
		LLMRegistry:             llmRegistry,
		LLMCommandPublisher:     llmCommandPublisher,
		ServiceCommandPublisher: serviceCommandPublisher,
		PackageCommandPublisher: packageCommandPublisher,
		WorkerCommandPublisher:  workerCommandPublisher,
		PackageProjection:       packageProjection,
		WorkerReadModels:        workerReadModelSvc,
		Workers:                 workerRepo,
		DNSEndpoints:            dnsProjector,
	}
	configureBackupMCPDeps(&mcpDeps, backupRegistryRepo, controlPlanePool, controlPlaneSigner, controlPlaneRelays)
	mcpServer := mcp.NewServerWithOptions(registry, logger, mcpDeps)
	mcpHandler := handlers.NewMCPHandler(mcpServer, logger)
	logger.Info("mcp server initialized")

	var assistantOrchestrator *service.AssistantOrchestrator
	var assistantIdentity service.AssistantIdentity
	if cfg.Assistant.Enabled {
		identity := bootstrapOperatorAssistant(ctx, cfg, controlPlaneRelays, logger)
		assistantIdentity = identity
		var assistantDNS service.AssistantDNSRegistry
		if dnsProjector != nil {
			assistantDNS = assistantDNSRegistryAdapter{endpoints: dnsProjector, zones: dnsZoneRepo, staticZones: dnsZones, policies: dnsPolicyRepo}
		}
		contextBuilder := service.NewAssistantContextBuilder(registry, llmRegistry, mlRegistry, assistantDNS, nil, service.AssistantContextBuilderConfig{})
		chatClient := llmadapter.NewChatClient(llmadapter.ChatClientConfig{
			BaseURL: cfg.Assistant.LLMBaseURL,
			Model:   cfg.Assistant.LLMModel,
			APIKey:  cfg.Assistant.LLMAPIKey,
		}, slog.Default())
		assistantOrchestrator = service.NewAssistantOrchestrator(service.AssistantOrchestratorConfig{
			ChatClient:       chatClient,
			ContextBuilder:   contextBuilder,
			ToolInvoker:      mcpServer,
			Publisher:        &auditedNostrPublisher{delegate: controlPlanePool, repo: nostrEventRepo, logger: logger},
			Subscriber:       assistantRelaySubscriber{pool: controlPlanePool},
			Signer:           controlPlaneSigner,
			Identity:         identity,
			AllowedToolNames: assistantToolNames(mcpServer),
			InitialSessions:  loadAssistantSessions(ctx, nostrEventRepo, logger),
			Logger:           slog.Default(),
		})
		servicePubkey := ""
		if strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
			servicePubkey, _ = nostr.GetPublicKey(cfg.Nostr.PrivateKey)
		}
		bgManager.RegisterWithOptions(service.NewAssistantSessionRecoveryRunner(assistantOrchestrator, service.AssistantSessionRecoveryConfig{RecentLimit: 500, ServicePubkey: servicePubkey, Logger: slog.Default()}), RunnerTier(Tier3))
		logger.Info("operator assistant orchestrator initialized", zap.String("agent_id", identity.AgentID), zap.String("assistant_pubkey", identity.Pubkey))
	}

	// Nostr inbound subscriber: listens for Hive-CI, Loom, and Bahia events.
	nostrSub := nostrAdapter.NewSubscriber(relayPool, nostrEventRepo, logger,
		nostrAdapter.WithHandler(nostrProcessor.Handle),
		nostrAdapter.WithAuthorizedAuthorScopes(controlPlaneSubscriberAuthorScopes(cfg, assistantIdentity)),
	)
	bgManager.RegisterWithOptions(nostrSub, RunnerTier(Tier1))

	// Encrypted request/result event runtime for sensitive browser route migrations.
	if len(controlPlaneRelays) > 0 && controlPlaneSigner != nil && cfg.Nostr.PrivateKey != "" {
		responder := controlplane.NewEncryptedResponder(controlPlanePool, controlPlaneSigner, cfg.Nostr.PrivateKey, logger)
		encryptedRequestTransport := controlplane.NewEncryptedRequestTransport(controlPlanePool, responder, cfg.Nostr.AuthorizedPubkeys, logger)
		controlplane.NewEncryptedDomainHandlers(controlplane.EncryptedDomainHandlersConfig{
			Payments:              paymentSvc,
			Orgs:                  orgRepo,
			Members:               orgMemberRepo,
			Invites:               orgInviteRepo,
			RBAC:                  auth.NewRBAC(orgMemberRepo),
			BootstrapOwnerPubkeys: cfg.Auth.BootstrapOwnerPubkeys,
			Logger:                logger,
		}).Register(encryptedRequestTransport)
		controlplane.NewEncryptedRouteHandlers(controlplane.EncryptedRouteHandlersConfig{
			Secrets:      secretRepo,
			Encryptor:    secretEncryptor,
			Runs:         runRepo,
			RunLogs:      runLogService,
			Artifacts:    artifactRepo,
			Signatures:   sigRepo,
			SignVerifier: signVerifier,
			Services:     serviceRepo,
			Intents:      intentRepo,
			RBAC:         auth.NewRBAC(orgMemberRepo),
			Logger:       logger,
		}).Register(encryptedRequestTransport)
		controlplane.RegisterNotificationEncryptedHandlers(encryptedRequestTransport, notifRepo, notifDispatcher)
		bgManager.RegisterWithOptions(&encryptedRequestTransportRunner{transport: encryptedRequestTransport}, RunnerTier(Tier2))
		logger.Info("encrypted request/result event runtime registered", zap.Strings("relay_urls_for_encrypted_nostr_requests", controlPlaneRelays))
	}

	// Nostr control plane reactor for event-driven deployment operations.
	if len(controlPlaneRelays) > 0 && controlPlaneSigner != nil {
		reactorConfig := controlplane.Config{
			Relays:                         controlPlaneRelays,
			PrivateKey:                     cfg.Nostr.PrivateKey,
			AuthorizedPubkeys:              controlPlaneAuthorizedPubkeys(cfg, assistantIdentity),
			AdoptionAuthorizedPubkeys:      cfg.Adoption.AllowedPubkeys,
			DirectRuntimeAuthorizedPubkeys: cfg.DirectRuntime.AllowedPubkeys,
		}
		// Reuse the single canonical control-plane signer for all control-plane
		// event signing paths.
		if cfg.Adoption.Enabled {
			if len(cfg.Nostr.AuthorizedPubkeys) == 0 && len(cfg.Adoption.AllowedPubkeys) == 0 {
				logger.Warn("signer-first adoption control plane has no pubkey allowlist", zap.Strings("relays", controlPlaneRelays))
			}
			if len(cfg.Adoption.AllowedSubjects) > 0 || len(cfg.Adoption.AllowedEmails) > 0 {
				logger.Warn("signer-first adoption control plane ignores non-pubkey operator allowlist entries", zap.Strings("allowed_subjects", cfg.Adoption.AllowedSubjects), zap.Strings("allowed_emails", cfg.Adoption.AllowedEmails))
			}
		}
		if cfg.DirectRuntime.Enabled {
			if len(cfg.Nostr.AuthorizedPubkeys) == 0 && len(cfg.DirectRuntime.AllowedPubkeys) == 0 {
				logger.Warn("signer-first direct-runtime control plane has no pubkey allowlist", zap.Strings("relays", controlPlaneRelays))
			}
			if len(cfg.DirectRuntime.AllowedSubjects) > 0 || len(cfg.DirectRuntime.AllowedEmails) > 0 {
				logger.Warn("signer-first direct-runtime control plane ignores non-pubkey operator allowlist entries", zap.Strings("allowed_subjects", cfg.DirectRuntime.AllowedSubjects), zap.Strings("allowed_emails", cfg.DirectRuntime.AllowedEmails))
			}
		}
		reactorOpts := appendControlPlaneAuditOption([]controlplane.ReactorOption{
			controlplane.WithBackupRegistry(backupRegistry),
			controlplane.WithBackupRunExecutor(backupCoordinator),
			controlplane.WithBackupRunResponder(backupResponder),
			controlplane.WithBackupRestoreExecutor(backupRestoreCoordinator),
			controlplane.WithBackupRestoreResponder(backupRestoreResponder),
			controlplane.WithBackupRetentionExecutor(backupRetentionCoordinator),
			controlplane.WithBackupRetentionResponder(backupRetentionResponder),
			controlplane.WithAdoptionService(adoptionSvc),
			controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc),
			controlplane.WithToolProvisioningRepository(toolProvisionRepo),
			controlplane.WithToolResponder(controlplane.NewToolResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo)),
			controlplane.WithToolProvisioningCoordinator(toolCoordinator),
			controlplane.WithPolicyService(policySvc),
			controlplane.WithMLRegistry(mlRegistry),
		}, nostrEventRepo)
		if llmRegistry != nil {
			reactorOpts = append(reactorOpts, controlplane.WithLLMRegistry(llmRegistry))
		}
		if assistantOrchestrator != nil {
			reactorOpts = append(reactorOpts, controlplane.WithAssistantOrchestrator(assistantOrchestrator))
		}
		if dnsOperator != nil {
			reactorOpts = append(reactorOpts, controlplane.WithDNSOperator(dnsOperator))
		}
		reactorOpts = append(reactorOpts, controlplane.WithWorkerRepository(workerRepo), controlplane.WithWorkerCleanupOrchestrator(workerCleanupOrchestrator))
		reactorOpts = appendPackageControlPlaneOptions(reactorOpts, packageRegistrySvc, packageProjection)
		reactor := controlplane.NewReactor(reactorConfig, registry, controlPlanePool, controlPlaneSigner, logger, reactorOpts...)
		bgManager.RegisterWithOptions(&controlplaneRunner{reactor: reactor}, RunnerTier(Tier2))
		logger.Info("nostr control plane reactor registered", zap.Strings("relays", controlPlaneRelays))
	}

	// Tenant RBAC.
	rbac := auth.NewRBAC(orgMemberRepo)
	var nip98Validator *auth.NIP98Validator
	var nip05Resolver *auth.NIP05Resolver
	if cfg.Auth.Enabled {
		nip98Validator = auth.NewNIP98Validator(auth.DefaultNIP98Config())
		if len(cfg.Adoption.AllowedEmails) > 0 || len(cfg.DirectRuntime.AllowedEmails) > 0 || len(cfg.LLM.AllowedEmails) > 0 {
			nip05Resolver = auth.NewNIP05Resolver()
		}
	}
	authMiddleware := auth.MiddlewareConfig{
		Enabled:        cfg.Auth.Enabled,
		NIP98Validator: nip98Validator,
		NIP05Resolver:  nip05Resolver,
	}

	// HTTP router.
	handler := router.NewWithDeps(registry, logger, cfg.CORS, telemetryProvider,
		router.RouterDeps{
			Config:             cfg,
			AuthMiddleware:     authMiddleware,
			Workers:            workerRepo,
			Builds:             buildRepo,
			Runs:               runRepo,
			Services:           serviceRepo,
			Environments:       envRepo,
			EnvStates:          stateRepo,
			RuntimeResolver:    runtimeResolver,
			Payments:           paymentSvc,
			SBOMs:              sbomRepo,
			Artifacts:          artifactRepo,
			Policies:           policySvc,
			Adoption:           adoptionSvc,
			RuntimeLifecycle:   runtimeLifecycleSvc,
			Secrets:            secretRepo,
			Encryptor:          secretEncryptor,
			Notifications:      notifRepo,
			Dispatcher:         notifDispatcher,
			MCP:                mcpHandler,
			Blossom:            blossomClient,
			OCI:                ociHandler,
			Orgs:               orgRepo,
			OrgMembers:         orgMemberRepo,
			OrgInvites:         orgInviteRepo,
			RBAC:               rbac,
			MLRegistry:         mlRegistry,
			MLCommands:         mlCommandPublisher,
			LLMRegistry:        llmRegistry,
			ContinuityStatuses: continuityStatusStore,
			ContinuityGraph:    continuityGraph,
			HealthProvider:     healthProvider,
			ModePolicy:         policy,
		}, cfg.Auth)

	httpServer := &http.Server{
		Addr:         cfg.ServerAddress(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	return &App{
		Config:             cfg,
		Logger:             logger,
		DB:                 pool,
		Registry:           registry,
		MLRegistry:         mlRegistry,
		LLMRegistry:        llmRegistry,
		HTTPServer:         httpServer,
		Publisher:          publisher,
		Coordinator:        coord,
		Reconciler:         rec,
		NostrPub:           nostrPub,
		Telemetry:          telemetryProvider,
		Background:         bgManager,
		toolCoordinator:    toolCoordinator,
			relayPools:         []*nostrAdapter.RelayPool{controlPlanePool, relayPool, fipsRelayPool},
		ModePolicy:         policy,
		Health:             healthProvider,
		RelayFirstRegistry: relayFirstRegistry,
	}, nil
}

func configuredMode(mode string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(mode))) {
	case ModeDegraded:
		return ModeDegraded
	case ModeEmergency:
		return ModeEmergency
	default:
		return ModeFull
	}
}

func connectOptionalDatabase(ctx context.Context, cfg *config.Config, logger *zap.Logger, policy *ModePolicy) (*pgxpool.Pool, bool) {
	pool, err := dbConnect(ctx, cfg.DB, logger)
	if err != nil {
		logger.Warn("postgres cache unavailable; continuing with relay-first reduced tier", zap.Error(err))
		if policy != nil && policy.ActiveTier > Tier1 {
			policy.SetActiveTier(Tier1)
		}
		return nil, false
	}
	if err := dbMigrate(ctx, pool, logger); err != nil {
		pool.Close()
		logger.Warn("postgres cache migration failed; continuing with relay-first reduced tier", zap.Error(err))
		if policy != nil && policy.ActiveTier > Tier1 {
			policy.SetActiveTier(Tier1)
		}
		return nil, false
	}
	return pool, true
}

func aggregateRelayHealth(pools ...*nostrAdapter.RelayPool) (connected, healthy int) {
	seen := make(map[*nostrAdapter.RelayPool]struct{}, len(pools))
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		if _, ok := seen[pool]; ok {
			continue
		}
		seen[pool] = struct{}{}
		connected += pool.ConnectedCount()
		healthy += pool.HealthyCount()
	}
	return connected, healthy
}

// Run starts the HTTP server and blocks until shutdown.
// bootstrapCacheAdapter bridges RelayProjectionCache (which takes any) to
// BootstrapCacheApplier (which takes *DecodedProjectionEvent).
type bootstrapCacheAdapter struct {
	cache *service.RelayProjectionCache
}

func (a *bootstrapCacheAdapter) Apply(ctx context.Context, event *nostrAdapter.DecodedProjectionEvent) error {
	return a.cache.Apply(ctx, event)
}

// newInMemoryProjectionMetaRepo creates a simple in-memory RelayProjectionMetaRepository
// for bootstrap ordering without requiring Postgres.
func newInMemoryProjectionMetaRepo() repository.RelayProjectionMetaRepository {
	return &inMemoryProjectionMetaRepo{store: make(map[string]*repository.RelayProjectionMeta)}
}

type inMemoryProjectionMetaRepo struct {
	mu    sync.RWMutex
	store map[string]*repository.RelayProjectionMeta
}

func (r *inMemoryProjectionMetaRepo) Get(_ context.Context, stream, entityKey string) (*repository.RelayProjectionMeta, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store[stream+"/"+entityKey], nil
}

func (r *inMemoryProjectionMetaRepo) Upsert(_ context.Context, meta repository.RelayProjectionMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := meta.Stream + "/" + meta.EntityKey
	if existing, ok := r.store[key]; ok && !meta.UpdatedAt.After(existing.UpdatedAt) {
		return nil
	}
	r.store[key] = &meta
	return nil
}

func (r *inMemoryProjectionMetaRepo) ListByStream(_ context.Context, stream string) ([]repository.RelayProjectionMeta, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []repository.RelayProjectionMeta
	for k, v := range r.store {
		if len(k) > len(stream)+1 && k[:len(stream)+1] == stream+"/" {
			result = append(result, *v)
		}
	}
	return result, nil
}

type bootstrapperRunner struct {
	bootstrapper    *nostrAdapter.Bootstrapper
	policy          *ModePolicy
	statusProjector *service.BahiaStatusProjector
	catalogVersion  string
}

func (r *bootstrapperRunner) Name() string { return "relay-bootstrapper" }
func (r *bootstrapperRunner) Run(ctx context.Context) error {
	if r.bootstrapper == nil {
		return fmt.Errorf("relay bootstrapper is not configured")
	}

	// Publish identity at startup.
	if r.statusProjector != nil {
		_ = r.statusProjector.PublishIdentity(ctx, service.BahiaIdentityPayload{
			Version:        "1.0.0",
			CatalogVersion: r.catalogVersion,
			Mode:           string(r.policy.RequestedMode),
			StartedAt:      time.Now().Unix(),
		})
	}

	err := r.bootstrapper.Run(ctx)
	if r.policy != nil && r.bootstrapper.ReadyTier() >= 0 {
		r.policy.SetActiveTier(Tier(r.bootstrapper.ReadyTier()))
	}

	// Publish checkpoint and readiness after bootstrap completes.
	if r.statusProjector != nil {
		progress := r.bootstrapper.Progress()
		_ = r.statusProjector.PublishCheckpoint(ctx, service.ReplayCheckpointPayload{
			CatalogVersion: r.catalogVersion,
			Phase:          string(progress.Phase),
		})
		_ = r.statusProjector.PublishReadiness(ctx, service.ReadinessStatusPayload{
			Phase:         string(progress.Phase),
			ActiveTier:    int(r.policy.ActiveTier),
			RequestedTier: int(r.policy.RequestedTier),
			Ready:         r.bootstrapper.Ready(),
		})
	}

	return err
}

type failoverTriggerRunner struct {
	engine *service.FailoverTriggerEngine
}

func (r *failoverTriggerRunner) Name() string { return "continuity-failover-trigger" }
func (r *failoverTriggerRunner) Run(ctx context.Context) error {
	if r.engine == nil {
		return fmt.Errorf("continuity failover trigger engine is not configured")
	}
	r.engine.Run(ctx)
	return nil
}

func setupContinuityRuntimeSubscriptions(
	publisher events.Publisher,
	definitions service.ContinuityDefinitionStore,
	heartbeats service.HeartbeatMonitor,
	executor service.ContinuityRecipeExecutor,
	trigger *service.FailoverTriggerEngine,
	logger *zap.Logger,
) {
	if publisher == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	publisher.Subscribe(events.EventContinuityProfileObserved, func(_ context.Context, e events.Event) {
		observed, ok := e.Data.(events.ContinuityProfileObserved)
		if !ok {
			logger.Warn("continuity profile event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		if _, err := definitions.StoreProfile(observed.Profile); err != nil {
			logger.Warn("store continuity profile failed", zap.String("service_key", observed.Profile.ServiceKey), zap.Error(err))
		}
	})
	publisher.Subscribe(events.EventFailoverPolicyObserved, func(_ context.Context, e events.Event) {
		observed, ok := e.Data.(events.ContinuityRecipeObserved)
		if !ok {
			logger.Warn("continuity failover policy event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		if _, err := definitions.StoreRecipe(observed.Recipe); err != nil {
			logger.Warn("store continuity failover recipe failed", zap.String("service_key", observed.Recipe.ServiceKey), zap.String("recipe", observed.Recipe.Name), zap.Error(err))
		}
	})
	publisher.Subscribe(events.EventReplicationPolicyObserved, func(_ context.Context, e events.Event) {
		observed, ok := e.Data.(events.ReplicationPolicyObserved)
		if !ok {
			logger.Warn("continuity replication policy event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		if _, err := definitions.StoreReplicationPolicy(observed.Policy); err != nil {
			logger.Warn("store continuity replication policy failed", zap.String("service_key", observed.Policy.ServiceKey), zap.Error(err))
		}
	})
	publisher.Subscribe(events.EventRecoveryWorkflowObserved, func(_ context.Context, e events.Event) {
		observed, ok := e.Data.(events.ContinuityRecipeObserved)
		if !ok {
			logger.Warn("continuity recovery workflow event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		if _, err := definitions.StoreRecipe(observed.Recipe); err != nil {
			logger.Warn("store continuity recovery recipe failed", zap.String("service_key", observed.Recipe.ServiceKey), zap.String("recipe", observed.Recipe.Name), zap.Error(err))
		}
	})
	publisher.Subscribe(events.EventHeartbeatObserved, func(_ context.Context, e events.Event) {
		observed, ok := e.Data.(events.HeartbeatObserved)
		if !ok {
			logger.Warn("heartbeat event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		heartbeats.Observe(observed.Observation)
	})
	publisher.Subscribe(events.EventFailoverRequested, func(ctx context.Context, e events.Event) {
		command, ok := e.Data.(events.ContinuityCommandRequested)
		if !ok {
			logger.Warn("failover command event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		executeContinuityFailoverCommand(ctx, definitions, executor, command, logger)
	})
	publisher.Subscribe(events.EventRecoveryRequested, func(ctx context.Context, e events.Event) {
		command, ok := e.Data.(events.ContinuityCommandRequested)
		if !ok {
			logger.Warn("recovery command event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		executeContinuityRecoveryCommand(ctx, definitions, executor, command, logger)
	})
	publisher.Subscribe(service.EventFailoverRequested, func(ctx context.Context, e events.Event) {
		request, ok := e.Data.(service.FailoverRequested)
		if !ok {
			logger.Warn("automatic failover request event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		executeAutomaticContinuityFailover(ctx, definitions, executor, request, logger)
	})
	publisher.Subscribe(service.EventContinuityRecipeRunStarted, func(_ context.Context, e events.Event) {
		progress, ok := e.Data.(service.ContinuityRecipeProgressEvent)
		if !ok || progress.RecipeKind != domain.ContinuityRecipeKindFailover || trigger == nil {
			return
		}
		trigger.MarkActive(progress.ServiceKey, progress.RunID, progress.StartedAt)
	})
}

func setupWorkerPressureSubscriptions(
	publisher events.Publisher,
	monitor *service.WorkerPressureMonitor,
	statePublisher *controlplane.WorkerStatePublisher,
	cleanupOrchestrator *service.WorkerCleanupOrchestrator,
	workerRepo repository.WorkerRepository,
	logger *zap.Logger,
) {
	if publisher == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	publisher.Subscribe(events.EventWorkerTelemetryObserved, func(ctx context.Context, e events.Event) {
		observed, ok := e.Data.(events.WorkerTelemetryObserved)
		if !ok {
			logger.Warn("worker telemetry event carried unsupported payload", zap.String("event_type", string(e.Type)))
			return
		}
		previous, current, changed := monitor.Observe(observed.Worker)
		if changed {
			publisher.Publish(ctx, events.Event{
				Type:     events.EventWorkerPressureChanged,
				EntityID: observed.Worker.PubKey,
				Data: events.WorkerPressureChanged{
					WorkerPubKey: observed.Worker.PubKey,
					Previous:     previous,
					Current:      current,
					ChangedAt:    time.Now().UTC(),
				},
			})
		}
		if statePublisher != nil {
			if err := statePublisher.Publish(ctx, &observed.Worker); err != nil {
				logger.Warn("publish worker state after telemetry observation failed", zap.String("worker_pubkey", observed.Worker.PubKey), zap.Error(err))
			}
		}
	})
	publisher.Subscribe(events.EventWorkerPressureChanged, func(ctx context.Context, e events.Event) {
		changed, ok := e.Data.(events.WorkerPressureChanged)
		if !ok || cleanupOrchestrator == nil || !cleanupOrchestrator.AutoModeEnabled() || changed.Current == nil {
			return
		}
		if changed.Current.CapacityClass != domain.WorkerCapacityCleanupOnly || changed.Current.RecommendedAction != domain.WorkerPressureActionCleanupRecommended {
			return
		}
		worker, err := workerRepo.GetByPubKey(ctx, changed.WorkerPubKey)
		if err != nil || worker == nil {
			if err != nil {
				logger.Warn("lookup worker for automatic cleanup failed", zap.String("worker_pubkey", changed.WorkerPubKey), zap.Error(err))
			}
			return
		}
		if !strings.EqualFold(worker.Labels["bahia.cleanup.auto"], "true") {
			return
		}
		go func() {
			if _, err := cleanupOrchestrator.RequestCleanup(context.Background(), changed.WorkerPubKey, service.CleanupModeReclaimableOnly, "pressure monitor cleanup recommendation"); err != nil {
				logger.Warn("automatic worker cleanup request failed", zap.String("worker_pubkey", changed.WorkerPubKey), zap.Error(err))
			}
		}()
	})
}

func executeContinuityFailoverCommand(ctx context.Context, definitions service.ContinuityDefinitionStore, executor service.ContinuityRecipeExecutor, command events.ContinuityCommandRequested, logger *zap.Logger) {
	if executor == nil || definitions == nil {
		logger.Warn("continuity failover command ignored because runtime is not configured", zap.String("service_key", command.ServiceKey))
		return
	}
	recipe, ok := continuityRecipeForCommand(definitions, command.ServiceKey, command.RecipeName, domain.ContinuityRecipeKindFailover)
	if !ok {
		logger.Warn("continuity failover recipe not found", zap.String("service_key", command.ServiceKey), zap.String("recipe", command.RecipeName))
		return
	}
	profile, _ := definitions.GetProfile(command.ServiceKey)
	if err := executor.ExecuteFailover(ctx, service.FailoverExecutionRequest{
		ServiceKey:            command.ServiceKey,
		RecipeName:            recipe.Name,
		TargetProfile:         command.TargetProfile,
		PrimaryWorkerPubKey:   profile.PrimaryWorkerPubKey,
		SelectedStandbyPubKey: command.TargetWorkerPubKey,
		RequestedBy:           command.Source.PubKey,
		RunID:                 continuityCommandRunID(command),
		Recipe:                recipe,
	}); err != nil {
		logger.Warn("execute continuity failover command failed", zap.String("service_key", command.ServiceKey), zap.String("run_id", continuityCommandRunID(command)), zap.Error(err))
	}
}

func executeContinuityRecoveryCommand(ctx context.Context, definitions service.ContinuityDefinitionStore, executor service.ContinuityRecipeExecutor, command events.ContinuityCommandRequested, logger *zap.Logger) {
	if executor == nil || definitions == nil {
		logger.Warn("continuity recovery command ignored because runtime is not configured", zap.String("service_key", command.ServiceKey))
		return
	}
	recipe, ok := continuityRecipeForCommand(definitions, command.ServiceKey, command.RecipeName, domain.ContinuityRecipeKindRecovery)
	if !ok {
		logger.Warn("continuity recovery recipe not found", zap.String("service_key", command.ServiceKey), zap.String("recipe", command.RecipeName))
		return
	}
	profile, _ := definitions.GetProfile(command.ServiceKey)
	if err := executor.ExecuteRecovery(ctx, service.RecoveryExecutionRequest{
		ServiceKey:            command.ServiceKey,
		RecipeName:            recipe.Name,
		TargetProfile:         command.TargetProfile,
		PrimaryWorkerPubKey:   profile.PrimaryWorkerPubKey,
		SelectedStandbyPubKey: command.TargetWorkerPubKey,
		RequestedBy:           command.Source.PubKey,
		RunID:                 continuityCommandRunID(command),
		Recipe:                recipe,
	}); err != nil {
		logger.Warn("execute continuity recovery command failed", zap.String("service_key", command.ServiceKey), zap.String("run_id", continuityCommandRunID(command)), zap.Error(err))
	}
}

func executeAutomaticContinuityFailover(ctx context.Context, definitions service.ContinuityDefinitionStore, executor service.ContinuityRecipeExecutor, request service.FailoverRequested, logger *zap.Logger) {
	if executor == nil || definitions == nil {
		logger.Warn("automatic continuity failover ignored because runtime is not configured", zap.String("service_key", request.ServiceKey), zap.String("run_id", request.RunID))
		return
	}
	recipe, ok := continuityRecipeForCommand(definitions, request.ServiceKey, request.RecipeName, domain.ContinuityRecipeKindFailover)
	if !ok {
		logger.Warn("automatic continuity failover recipe not found", zap.String("service_key", request.ServiceKey), zap.String("recipe", request.RecipeName), zap.String("run_id", request.RunID))
		return
	}
	if err := executor.ExecuteFailover(ctx, service.FailoverExecutionRequest{
		ServiceKey:            request.ServiceKey,
		RecipeName:            recipe.Name,
		TargetProfile:         domain.ContinuityModeDegraded,
		PrimaryWorkerPubKey:   request.PrimaryWorkerPubKey,
		SelectedStandbyPubKey: request.StandbyWorkerPubKey,
		RequestedBy:           "continuity-failover-trigger",
		RunID:                 request.RunID,
		Recipe:                recipe,
	}); err != nil {
		logger.Warn("execute automatic continuity failover failed", zap.String("service_key", request.ServiceKey), zap.String("run_id", request.RunID), zap.Error(err))
	}
}

func continuityRecipeForCommand(definitions service.ContinuityDefinitionStore, serviceKey string, recipeName string, kind domain.ContinuityRecipeKind) (domain.ContinuityRecipe, bool) {
	if strings.TrimSpace(recipeName) != "" {
		recipe, ok := definitions.GetRecipe(serviceKey, kind)
		if !ok || recipe.Name != strings.TrimSpace(recipeName) {
			return domain.ContinuityRecipe{}, false
		}
		return recipe, true
	}
	return definitions.GetRecipe(serviceKey, kind)
}

func continuityCommandRunID(command events.ContinuityCommandRequested) string {
	if strings.TrimSpace(command.IdempotencyKey) != "" {
		return strings.TrimSpace(command.IdempotencyKey)
	}
	if strings.TrimSpace(command.Source.EventID) != "" {
		return strings.TrimSpace(command.Source.EventID)
	}
	return fmt.Sprintf("continuity:%s:%d", strings.TrimSpace(command.ServiceKey), command.Source.CreatedAt.UnixNano())
}

type loomCleanupClient struct {
	client *loom.Client
}

func (c loomCleanupClient) SubmitCleanupJob(ctx context.Context, job service.CleanupJobRequest) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("loom client is not configured")
	}
	return c.client.SubmitJob(ctx, loom.JobRequest{ID: job.ID, Type: job.Type, WorkerPubkey: job.WorkerPubkey, Cmd: job.Cmd, Args: job.Args, Env: job.Env, PaymentToken: job.PaymentToken})
}

func (c loomCleanupClient) PollCleanupJobStatusFromWorker(ctx context.Context, jobEventID string, expectedWorkerPubkey string, callbacks ...service.CleanupStatusCallback) (*service.CleanupJobStatus, error) {
	if c.client == nil {
		return nil, fmt.Errorf("loom client is not configured")
	}
	loomCallbacks := make([]loom.StatusCallback, 0, len(callbacks))
	for _, cb := range callbacks {
		callback := cb
		loomCallbacks = append(loomCallbacks, func(status *loom.JobStatus) {
			if callback != nil {
				callback(cleanupJobStatusFromLoom(status))
			}
		})
	}
	status, err := c.client.PollJobStatusFromWorker(ctx, jobEventID, expectedWorkerPubkey, loomCallbacks...)
	return cleanupJobStatusFromLoom(status), err
}

func cleanupJobStatusFromLoom(status *loom.JobStatus) *service.CleanupJobStatus {
	if status == nil {
		return nil
	}
	return &service.CleanupJobStatus{JobID: status.JobID, Status: status.Status, Success: status.Success, ExitCode: status.ExitCode, Duration: status.Duration, WorkerPubkey: status.WorkerPubkey, StdoutURL: status.StdoutURL, StderrURL: status.StderrURL, ChangeToken: status.ChangeToken, Error: status.Error, LogOutput: status.LogOutput}
}

type continuityWorkerReader struct {
	repo   repository.WorkerRepository
	logger *zap.Logger
}

func startBackgroundRunners(ctx context.Context, manager *BackgroundManager, policy *ModePolicy) {
	if manager == nil {
		return
	}
	if policy == nil {
		policy = NewModePolicy(ModeFull)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()

	for _, reg := range manager.runners {
		if !policy.RunnerEnabled(Tier(reg.tier)) {
			manager.logger.Info("background runner gated by active tier", zap.String("name", reg.runner.Name()), zap.Int("runner_tier", reg.tier), zap.Int("active_tier", int(policy.ActiveTier)))
			continue
		}
		manager.wg.Add(1)
		go func(reg backgroundRunnerRegistration) {
			runner := reg.runner
			defer manager.wg.Done()
			manager.logger.Info("background runner starting", zap.String("name", runner.Name()))
			manager.markRunnerStarted(runner.Name())

			err := runner.Run(ctx)
			manager.markRunnerStopped(runner.Name(), err, ctx.Err() != nil)
			if err != nil && ctx.Err() == nil {
				manager.logger.Error("background runner exited with error", zap.String("name", runner.Name()), zap.Error(err))
			} else {
				manager.logger.Info("background runner stopped", zap.String("name", runner.Name()))
			}
		}(reg)
	}
}

func (r continuityWorkerReader) ListWorkers() []domain.Worker {
	if r.repo == nil {
		return nil
	}
	workers, err := r.repo.List(context.Background(), "", 0)
	if err != nil {
		if r.logger != nil {
			r.logger.Warn("list workers for continuity graph failed", zap.Error(err))
		}
		return nil
	}
	return workers
}

func (a *App) Run() error {
	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		a.Logger.Info("HTTP server starting", zap.String("addr", a.HTTPServer.Addr))
		if err := a.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Start allowed background runners after HTTP is accepting connections.
	startBackgroundRunners(ctx, a.Background, a.ModePolicy)

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
	closeRelayPools(a.relayPools...)

	if a.DB != nil {
		a.DB.Close()
	}
	_ = a.Logger.Sync()
	a.Logger.Info("server stopped gracefully")
	return nil
}

type continuityDNSStatusReader struct {
	reader service.ContinuityStatusReader
}

func (r continuityDNSStatusReader) GetServiceContinuityStatus(serviceKey string) (*reconcile.ContinuityStatus, bool) {
	if r.reader == nil {
		return nil, false
	}
	status, ok := r.reader.GetServiceContinuityStatus(serviceKey)
	if !ok || status == nil {
		return nil, false
	}
	return &reconcile.ContinuityStatus{
		ServiceKey:         status.ServiceKey,
		ActiveProfile:      status.ActiveProfile,
		OperationState:     status.OperationState,
		ActiveWorkerPubKey: status.ActiveWorkerPubKey,
	}, true
}

type dnsResolverBridge struct {
	resolver dnsAdapter.Resolver
}

func (r dnsResolverBridge) Resolve(ref string) (reconcile.DNSBackend, bool) {
	if r.resolver == nil {
		return nil, false
	}
	backend, ok := r.resolver.Resolve(ref)
	if !ok {
		return nil, false
	}
	return backend, true
}

type staticDNSZoneProjectionSource struct {
	zones []domain.DNSZone
}

func (s staticDNSZoneProjectionSource) ListDNSZones() []domain.DNSZone {
	return append([]domain.DNSZone(nil), s.zones...)
}

type assistantDNSRegistryAdapter struct {
	endpoints interface {
		ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
	}
	zones       repository.DNSZoneRepository
	staticZones []domain.DNSZone
	policies    repository.DNSPolicyRepository
}

func (a assistantDNSRegistryAdapter) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	if a.endpoints == nil {
		return nil, nil
	}
	return a.endpoints.ListDNSEndpoints(ctx)
}

func (a assistantDNSRegistryAdapter) ListDNSZones(ctx context.Context) ([]domain.DNSZone, error) {
	if a.zones != nil {
		return a.zones.List(ctx)
	}
	return append([]domain.DNSZone(nil), a.staticZones...), nil
}

func (a assistantDNSRegistryAdapter) ListDNSPolicies(ctx context.Context) ([]domain.DNSPolicy, error) {
	if a.policies == nil {
		return nil, nil
	}
	return a.policies.List(ctx)
}

type configDNSBackendProjectionSource struct {
	backends map[string]config.DNSBackendConfig
	zones    []domain.DNSZone
	resolver dnsAdapter.Resolver
}

func (s configDNSBackendProjectionSource) ListDNSBackendStates(ctx context.Context) []domain.DNSBackendState {
	refs := make([]string, 0, len(s.backends))
	for ref := range s.backends {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	zoneRefsByBackend := make(map[string][]string, len(refs))
	for _, zone := range s.zones {
		zoneRefsByBackend[zone.BackendRef] = append(zoneRefsByBackend[zone.BackendRef], zone.Name)
	}
	states := make([]domain.DNSBackendState, 0, len(refs))
	now := time.Now().UTC()
	for _, ref := range refs {
		backendConfig := s.backends[ref]
		backendType := domain.DNSBackendType(strings.TrimSpace(backendConfig.Type))
		health := domain.HealthStatusUnknown
		if s.resolver != nil {
			if backend, ok := s.resolver.Resolve(ref); ok {
				backendType = backend.BackendType()
				if err := backend.Health(ctx); err != nil {
					health = domain.HealthStatusUnhealthy
				} else {
					health = domain.HealthStatusHealthy
				}
			}
		}
		zoneRefs := append([]string(nil), zoneRefsByBackend[ref]...)
		sort.Strings(zoneRefs)
		states = append(states, domain.DNSBackendState{Ref: ref, Type: backendType, Health: health, ZoneRefs: zoneRefs, UpdatedAt: now})
	}
	return states
}

type dnsControlPlaneOperator struct {
	reconciler *reconcile.DNSReconciler
	zones      map[string]struct{}
}

func newDNSControlPlaneOperator(reconciler *reconcile.DNSReconciler, zones []domain.DNSZone, persistence controlplane.DNSPersistenceOperator) controlplane.DNSControlPlaneOperator {
	zoneSet := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		zoneSet[strings.TrimSpace(zone.Name)] = struct{}{}
	}
	operator := &dnsControlPlaneOperator{reconciler: reconciler, zones: zoneSet}
	if persistence != nil {
		return &dnsPersistentControlPlaneOperator{dnsControlPlaneOperator: operator, persistence: persistence}
	}
	return operator
}

func (o *dnsControlPlaneOperator) ReconcileAll(ctx context.Context) error {
	if o.reconciler == nil {
		return fmt.Errorf("DNS reconciler is not configured")
	}
	return o.reconciler.ReconcileOnce(ctx)
}

func (o *dnsControlPlaneOperator) ReconcileZone(ctx context.Context, zoneName string) error {
	zoneName = strings.TrimSpace(zoneName)
	if zoneName == "" {
		return fmt.Errorf("DNS zone is required")
	}
	if !o.HasZone(zoneName) {
		return fmt.Errorf("DNS zone %q is not configured", zoneName)
	}
	return o.ReconcileAll(ctx)
}

func (o *dnsControlPlaneOperator) HasZone(zoneName string) bool {
	_, ok := o.zones[strings.TrimSpace(zoneName)]
	return ok
}

type dnsPersistentControlPlaneOperator struct {
	*dnsControlPlaneOperator
	persistence controlplane.DNSPersistenceOperator
}

func (o *dnsPersistentControlPlaneOperator) CreateZone(ctx context.Context, zone domain.DNSZone) error {
	if err := o.persistence.CreateZone(ctx, zone); err != nil {
		return err
	}
	if o.zones == nil {
		o.zones = map[string]struct{}{}
	}
	o.zones[strings.TrimSpace(zone.Name)] = struct{}{}
	return nil
}

func (o *dnsPersistentControlPlaneOperator) CreateOverride(ctx context.Context, override domain.DNSRecordOverride) error {
	return o.persistence.CreateOverride(ctx, override)
}

func (o *dnsPersistentControlPlaneOperator) ListOverridesByZone(ctx context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	return o.persistence.ListOverridesByZone(ctx, zoneName)
}

type dnsRepositoryPersistenceAdapter struct {
	zones     repository.DNSZoneRepository
	overrides repository.DNSRecordOverrideRepository
}

func (a dnsRepositoryPersistenceAdapter) CreateZone(ctx context.Context, zone domain.DNSZone) error {
	if a.zones == nil {
		return fmt.Errorf("DNS zone repository is not configured")
	}
	return a.zones.Create(ctx, &zone)
}

func (a dnsRepositoryPersistenceAdapter) CreateOverride(ctx context.Context, override domain.DNSRecordOverride) error {
	if a.overrides == nil {
		return fmt.Errorf("DNS record override repository is not configured")
	}
	return a.overrides.Create(ctx, &override)
}

func (a dnsRepositoryPersistenceAdapter) ListOverridesByZone(ctx context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	if a.overrides == nil {
		return nil, fmt.Errorf("DNS record override repository is not configured")
	}
	return a.overrides.ListByZone(ctx, zoneName)
}

func buildDNSRuntime(ctx context.Context, cfg config.DNSConfig, logger *zap.Logger) ([]domain.DNSZone, *dnsAdapter.StaticResolver, error) {
	zones := make([]domain.DNSZone, 0, len(cfg.Zones))
	for _, zoneConfig := range cfg.Zones {
		ttl := zoneConfig.TTL
		if ttl <= 0 {
			ttl = cfg.DefaultTTL
		}
		zone := domain.DNSZone{
			Name:       strings.TrimSpace(zoneConfig.Name),
			Visibility: domain.ZoneVisibility(strings.TrimSpace(zoneConfig.Visibility)),
			BackendRef: strings.TrimSpace(zoneConfig.Backend),
			TTL:        ttl,
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			return nil, nil, fmt.Errorf("configuring DNS zone %q: %w", zoneConfig.Name, err)
		}
		zones = append(zones, zone)
	}

	backendRefs := make([]string, 0, len(cfg.Backends))
	for ref := range cfg.Backends {
		backendRefs = append(backendRefs, ref)
	}
	sort.Strings(backendRefs)
	registrations := make([]dnsAdapter.BackendRegistration, 0, len(backendRefs))
	for _, ref := range backendRefs {
		backendConfig := cfg.Backends[ref]
		switch strings.TrimSpace(backendConfig.Type) {
		case string(domain.DNSBackendTypeFilesystem):
			rootDir := strings.TrimSpace(backendConfig.RootDir)
			if err := os.MkdirAll(rootDir, 0o755); err != nil {
				return nil, nil, fmt.Errorf("preparing DNS filesystem backend %q root %q: %w", ref, rootDir, err)
			}
			backend := dnsAdapter.NewFilesystemBackend(rootDir)
			if err := backend.Health(ctx); err != nil {
				return nil, nil, fmt.Errorf("checking DNS filesystem backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeCoreDNS):
			backend, err := dnsAdapter.NewCoreDNSBackend(dnsAdapter.CoreDNSConfig{EtcdEndpoints: backendConfig.EtcdEndpoints, EtcdPrefix: backendConfig.EtcdPrefix, DialTimeout: backendConfig.EtcdDialTimeout})
			if err != nil {
				return nil, nil, fmt.Errorf("configuring DNS CoreDNS backend %q: %w", ref, err)
			}
			if err := backend.Health(ctx); err != nil {
				return nil, nil, fmt.Errorf("checking DNS CoreDNS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypePowerDNS):
			backend, err := dnsAdapter.NewPowerDNSBackend(dnsAdapter.PowerDNSConfig{APIURL: backendConfig.PowerDNSAPIURL, APIKey: backendConfig.PowerDNSAPIKey, ServerID: backendConfig.PowerDNSServerID})
			if err != nil {
				return nil, nil, fmt.Errorf("configuring DNS PowerDNS backend %q: %w", ref, err)
			}
			if err := backend.Health(ctx); err != nil {
				return nil, nil, fmt.Errorf("checking DNS PowerDNS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeDNSMasq):
			backend := dnsAdapter.NewDnsmasqBackend(dnsAdapter.DnsmasqConfig{ConfigDir: backendConfig.DnsmasqConfigDir, ReloadCommand: backendConfig.DnsmasqReloadCommand, FilePrefix: backendConfig.DnsmasqFilePrefix})
			if err := backend.Health(ctx); err != nil {
				return nil, nil, fmt.Errorf("checking DNS dnsmasq backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeFIPS):
			backend := dnsAdapter.NewFIPSBackend(backendConfig.HostsPath, logger)
			if err := backend.Health(ctx); err != nil {
				return nil, nil, fmt.Errorf("checking DNS FIPS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		default:
			return nil, nil, fmt.Errorf("configuring DNS backend %q: unsupported type %q", ref, backendConfig.Type)
		}
	}
	resolver, err := dnsAdapter.NewStaticResolver(registrations...)
	if err != nil {
		return nil, nil, fmt.Errorf("configuring DNS backend resolver: %w", err)
	}
	for _, zone := range zones {
		if _, ok := resolver.Resolve(zone.BackendRef); !ok {
			return nil, nil, fmt.Errorf("configuring DNS zone %q: backend %q is not registered", zone.Name, zone.BackendRef)
		}
	}
	if logger != nil {
		logger.Info("DNS runtime configured", zap.Int("zones", len(zones)), zap.Strings("backends", resolver.Refs()))
	}
	return zones, resolver, nil
}

func controlPlaneRelayURLs(cfg config.NostrConfig) []string {
	if cfg.Sidecar.Enabled {
		if cfg.Sidecar.BackendURL != "" {
			return []string{cfg.Sidecar.BackendURL}
		}
		if cfg.Sidecar.PublicURL != "" {
			return []string{cfg.Sidecar.PublicURL}
		}
	}
	return append([]string(nil), cfg.Relays...)
}

func interopRelayURLs(cfg *config.Config, controlPlaneRelays []string) []string {
	var relays []string
	if cfg.Nostr.Sidecar.Enabled && cfg.Nostr.Sidecar.MirrorExternal {
		// The sidecar is the upstream mirror boundary. Subscribe through it for
		// public interop/audit traffic instead of also connecting Bahia directly to
		// cfg.Nostr.Relays, which would create duplicate publish/subscribe loops.
		for _, r := range controlPlaneRelays {
			relays = appendUniqueRelay(relays, r)
		}
	} else {
		for _, r := range cfg.Nostr.Relays {
			relays = appendUniqueRelay(relays, r)
		}
	}
	for _, r := range cfg.Loom.Relays {
		relays = appendUniqueRelay(relays, r)
	}
	return relays
}

func compactBootstrapAuthors(groups ...[]string) []string {
	seen := make(map[string]struct{})
	authors := make([]string, 0)
	for _, group := range groups {
		for _, raw := range group {
			value := strings.TrimSpace(raw)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			authors = append(authors, value)
		}
	}
	if len(authors) == 0 {
		return nil
	}
	return authors
}

func llmGatewayHTTPConfig(cfg config.LLMControlplaneConfig) llmadapter.GatewayHTTPConfig {
	endpoints := make(map[string]llmadapter.GatewayHTTPEndpointConfig, len(cfg.Gateways))
	for ref, ep := range cfg.Gateways {
		endpoints[ref] = llmadapter.GatewayHTTPEndpointConfig{Type: ep.Type, BaseURL: ep.BaseURL, AuthToken: ep.AuthToken, Timeout: ep.Timeout}
	}
	return llmadapter.GatewayHTTPConfig{Endpoints: endpoints}
}

type fipsSubscriberLifecycle interface {
	Start(context.Context) error
	Stop()
	Name() string
}

type fipsSubscriberRunner struct {
	subscriber fipsSubscriberLifecycle
}

func (r *fipsSubscriberRunner) Name() string {
	if r.subscriber == nil {
		return "fips-subscriber"
	}
	return r.subscriber.Name()
}

func (r *fipsSubscriberRunner) Run(ctx context.Context) error {
	if r.subscriber == nil {
		return fmt.Errorf("fips subscriber is not configured")
	}
	if err := r.subscriber.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	r.subscriber.Stop()
	return nil
}

func closeRelayPools(pools ...*nostrAdapter.RelayPool) {
	seen := make(map[*nostrAdapter.RelayPool]struct{}, len(pools))
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		if _, ok := seen[pool]; ok {
			continue
		}
		seen[pool] = struct{}{}
		pool.Close()
	}
}

func appendUniqueRelay(relays []string, relay string) []string {
	if relay == "" {
		return relays
	}
	for _, existing := range relays {
		if existing == relay {
			return relays
		}
	}
	return append(relays, relay)
}

func workerPressureThresholds(cfg config.WorkerPressureConfig) service.WorkerPressureThresholds {
	toBytes := func(gb int) int64 {
		if gb <= 0 {
			return 0
		}
		return int64(gb) * 1024 * 1024 * 1024
	}
	return service.EffectiveWorkerPressureThresholds(service.WorkerPressureThresholds{
		MemoryWarningMinBytes:  toBytes(cfg.MemoryWarningMinGB),
		MemoryWarningMinRatio:  cfg.MemoryWarningRatio,
		MemoryCriticalMinBytes: toBytes(cfg.MemoryCriticalMinGB),
		MemoryCriticalMinRatio: cfg.MemoryCriticalRatio,
		DiskWarningMinBytes:    toBytes(cfg.DiskWarningMinGB),
		DiskWarningMinRatio:    cfg.DiskWarningRatio,
		DiskCriticalMinBytes:   toBytes(cfg.DiskCriticalMinGB),
		DiskCriticalMinRatio:   cfg.DiskCriticalRatio,
		VRAMWarningMinBytes:    toBytes(cfg.VRAMWarningMinGB),
		VRAMWarningMinRatio:    cfg.VRAMWarningRatio,
		VRAMCriticalMinBytes:   toBytes(cfg.VRAMCriticalMinGB),
		VRAMCriticalMinRatio:   cfg.VRAMCriticalRatio,
		ThermalWarningC:        cfg.ThermalWarningC,
		ThermalCriticalC:       cfg.ThermalCriticalC,
		QueueWarningRatio:      cfg.QueueWarningRatio,
		QueueCriticalRatio:     cfg.QueueCriticalRatio,
	})
}

func runtimeRegistryAuth(cfg *config.Config) *runtime.RegistryAuthConfig {
	if cfg == nil {
		return nil
	}
	if cfg.Registry.URL != "" && cfg.Registry.Username != "" && cfg.Registry.Password != "" {
		return &runtime.RegistryAuthConfig{
			Server:   cfg.Registry.URL,
			Username: cfg.Registry.Username,
			Password: cfg.Registry.Password,
		}
	}
	if cfg.Harbor.Enabled && cfg.Harbor.URL != "" && cfg.Harbor.Username != "" && cfg.Harbor.Password != "" {
		return &runtime.RegistryAuthConfig{
			Server:   cfg.Harbor.URL,
			Username: cfg.Harbor.Username,
			Password: cfg.Harbor.Password,
		}
	}
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

type assistantRelaySubscriber struct {
	pool *nostrAdapter.RelayPool
}

func (s assistantRelaySubscriber) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (service.AssistantMergedSubscription, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("assistant relay pool is not configured")
	}
	merged, err := s.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return assistantMergedSubscription{merged: merged}, nil
}

type assistantMergedSubscription struct {
	merged *nostrAdapter.MergedSubscription
}

func (s assistantMergedSubscription) EventChan() <-chan *nostr.Event {
	if s.merged == nil {
		ch := make(chan *nostr.Event)
		close(ch)
		return ch
	}
	return s.merged.Events
}

func (s assistantMergedSubscription) ClosedChan() <-chan service.AssistantRelayClosed {
	out := make(chan service.AssistantRelayClosed, 16)
	if s.merged == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for closed := range s.merged.Closed {
			out <- service.AssistantRelayClosed{RelayURL: closed.RelayURL, Reason: closed.Reason}
		}
	}()
	return out
}

func (s assistantMergedSubscription) EOSEChan() <-chan struct{} {
	if s.merged == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.merged.EndOfStoredEvents
}

func (s assistantMergedSubscription) Close() {
	if s.merged != nil {
		s.merged.Close()
	}
}

type auditedNostrPublisher struct {
	delegate controlplane.NostrEventPublisher
	repo     repository.NostrEventRepository
	logger   *zap.Logger
}

type relayFirstNostrPublisher struct {
	pool *nostrAdapter.RelayPool
}

func (p relayFirstNostrPublisher) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if p.pool == nil {
		return 0, fmt.Errorf("relay pool is not configured")
	}
	return p.pool.Publish(ctx, ev)
}

func (p *auditedNostrPublisher) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if p == nil || p.delegate == nil {
		return 0, fmt.Errorf("audited nostr publisher delegate is not configured")
	}
	published, err := p.delegate.Publish(ctx, ev)
	if err == nil && published > 0 && p.repo != nil {
		tagsJSON, marshalErr := json.Marshal(ev.Tags)
		if marshalErr != nil {
			if p.logger != nil {
				p.logger.Warn("failed to marshal audited assistant event tags", zap.String("event_id", ev.ID), zap.Error(marshalErr))
			}
		} else if _, recordErr := p.repo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tagsJSON, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC()}); recordErr != nil && p.logger != nil {
			p.logger.Warn("failed to audit assistant event", zap.String("event_id", ev.ID), zap.Error(recordErr))
		}
	}
	return published, err
}

func appendControlPlaneAuditOption(opts []controlplane.ReactorOption, repo repository.NostrEventRepository) []controlplane.ReactorOption {
	if repo == nil {
		return opts
	}
	return append(opts, controlplane.WithNostrEventRepository(repo))
}

func configureBackupMCPDeps(deps *mcp.ServerDeps, readModels mcp.BackupReadModelRepository, publisher controlplane.NostrEventPublisher, signer canonicalnostr.Signer, relays []string) {
	if deps == nil {
		return
	}
	deps.BackupReadModels = readModels
	if publisher != nil && signer != nil && len(relays) > 0 {
		deps.BackupCommandPublisher = mcp.NewBackupCommandPublisher(publisher, signer)
	}
}

func appendPackageControlPlaneOptions(opts []controlplane.ReactorOption, packageRegistrySvc *service.PackageRegistryService, packageProjection repository.PackageControlPlaneRepository) []controlplane.ReactorOption {
	if packageRegistrySvc == nil {
		return opts
	}
	return append(opts,
		controlplane.WithPackageRegistryService(packageRegistrySvc),
		controlplane.WithPackageProjectionRepository(packageProjection),
	)
}

func controlPlaneSubscriberAuthorScopes(cfg *config.Config, assistant service.AssistantIdentity) nostrAdapter.AuthorizedAuthorScopes {
	var adoption []string
	var directRuntime []string
	if cfg != nil {
		adoption = cfg.Adoption.AllowedPubkeys
		directRuntime = cfg.DirectRuntime.AllowedPubkeys
	}
	return nostrAdapter.AuthorizedAuthorScopes{
		Default:       controlPlaneAuthorizedPubkeys(cfg, assistant),
		Adoption:      adoption,
		DirectRuntime: directRuntime,
	}
}

func controlPlaneAuthorizedPubkeys(cfg *config.Config, assistant service.AssistantIdentity) []string {
	seen := map[string]struct{}{}
	out := []string{}
	add := func(pubkey string) {
		pubkey = strings.ToLower(strings.TrimSpace(pubkey))
		if pubkey == "" {
			return
		}
		if _, ok := seen[pubkey]; ok {
			return
		}
		seen[pubkey] = struct{}{}
		out = append(out, pubkey)
	}
	if cfg != nil {
		for _, pubkey := range cfg.Nostr.AuthorizedPubkeys {
			add(pubkey)
		}
		if cfg.Assistant.Enabled && strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
			if pubkey, err := nostr.GetPublicKey(cfg.Nostr.PrivateKey); err == nil {
				add(pubkey)
			}
		}
	}
	add(assistant.Pubkey)
	return out
}

func assistantToolNames(server *mcp.Server) []string {
	if server == nil {
		return nil
	}
	names := []string{}
	for _, tool := range server.GetTools() {
		if strings.HasPrefix(tool.Name, "bahia_assistant_") {
			names = append(names, tool.Name)
		}
	}
	return names
}

func bootstrapOperatorAssistant(ctx context.Context, cfg *config.Config, relays []string, logger *zap.Logger) service.AssistantIdentity {
	identity := service.AssistantIdentity{AgentID: soulfactory.OperatorAssistantAgentID}
	if cfg != nil && strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
		if pubkey, err := nostr.GetPublicKey(cfg.Nostr.PrivateKey); err == nil {
			identity.Pubkey = pubkey
		}
	}
	if cfg == nil || (!cfg.Assistant.SignetAllowMock && strings.TrimSpace(cfg.Assistant.SignetBunkerURI) == "") {
		return identity
	}
	slogLogger := slog.Default()
	signetClient, err := signetAdapter.NewClient(signetAdapter.Config{BunkerURI: cfg.Assistant.SignetBunkerURI, Relays: relays, AllowMock: cfg.Assistant.SignetAllowMock}, slogLogger)
	if err != nil {
		logger.Warn("operator assistant signet client initialization failed; using service-key attribution fallback", zap.Error(err))
		return identity
	}
	if err := signetClient.Connect(ctx); err != nil {
		logger.Warn("operator assistant signet connection failed; using service-key attribution fallback", zap.Error(err))
		return identity
	}
	soulReactor := soulfactory.NewReactor(soulfactory.Config{Relays: relays, AuthorizedPubkeys: cfg.Nostr.AuthorizedPubkeys}, nil, signetClient, slogLogger)
	bootstrapped, err := soulfactory.EnsureOperatorAssistantSoul(ctx, soulReactor)
	if err != nil {
		logger.Warn("operator assistant soul bootstrap failed; using service-key attribution fallback", zap.Error(err))
		return identity
	}
	return service.AssistantIdentity{AgentID: bootstrapped.AgentID, Pubkey: bootstrapped.Pubkey, Npub: bootstrapped.Npub}
}

func loadAssistantSessions(ctx context.Context, repo repository.NostrEventRepository, logger *zap.Logger) []domain.AssistantSession {
	if repo == nil {
		return nil
	}
	records, err := repo.ListByKind(ctx, domain.KindAssistantSession, 500)
	if err != nil {
		logger.Warn("failed to load assistant session read models", zap.Error(err))
		return nil
	}
	seen := map[string]struct{}{}
	sessions := []domain.AssistantSession{}
	for _, record := range records {
		var session domain.AssistantSession
		if err := json.Unmarshal([]byte(record.Content), &session); err != nil {
			logger.Warn("failed to parse assistant session read model", zap.String("event_id", record.ID), zap.Error(err))
			continue
		}
		if session.SessionID == "" {
			continue
		}
		if _, ok := seen[session.SessionID]; ok {
			continue
		}
		seen[session.SessionID] = struct{}{}
		sessions = append(sessions, session)
	}
	return sessions
}

// controlplaneRunner adapts the controlplane.Reactor to the BackgroundRunner interface.
type controlplaneRunner struct {
	reactor *controlplane.Reactor
}

func (r *controlplaneRunner) Name() string { return "controlplane" }
func (r *controlplaneRunner) Run(ctx context.Context) error {
	return r.reactor.Run(ctx)
}

type encryptedRequestTransportRunner struct {
	transport *controlplane.EncryptedRequestTransport
}

func (r *encryptedRequestTransportRunner) Name() string { return "encrypted-request-result-events" }
func (r *encryptedRequestTransportRunner) Run(ctx context.Context) error {
	return r.transport.Run(ctx)
}
