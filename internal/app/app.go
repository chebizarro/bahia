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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/build"
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
	Config          *config.Config
	Logger          *zap.Logger
	DB              *pgxpool.Pool
	Registry        *service.RegistryService
	MLRegistry      *service.MLRegistryService
	LLMRegistry     *service.LLMRegistryService
	HTTPServer      *http.Server
	Publisher       events.Publisher
	Coordinator     *workflow.Coordinator
	Reconciler      *reconcile.Reconciler
	NostrPub        *nostrAdapter.Publisher
	Telemetry       *telemetry.Provider
	Background      *BackgroundManager
	toolCoordinator *service.ToolProvisioningCoordinator
	relayPools      []*nostrAdapter.RelayPool
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
	toolProvisionRepo := repository.NewPgToolProvisioningRepository(pool)

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

	// Tenant repositories.
	orgRepo := repository.NewPgOrganizationRepository(pool)
	orgMemberRepo := repository.NewPgOrgMemberRepository(pool)
	orgInviteRepo := repository.NewPgOrgInviteRepository(pool)

	// Event publisher.
	publisher := events.NewInProcessPublisher(logger)

	// Adapters.
	// Sidecar-first topology uses a dedicated relay pool for Bahia's own
	// control-plane/projector publishes. Interop relays use a separate pool so
	// optional sidecar upstream mirroring cannot create duplicate publish loops.
	controlPlaneRelays := controlPlaneRelayURLs(cfg.Nostr)
	controlPlanePool := nostrAdapter.NewRelayPool(controlPlaneRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
	controlPlanePool.Connect(ctx)

	encryptedRequestRelays := encryptedRequestRelayURLs(cfg.Nostr)
	var encryptedRequestPool *nostrAdapter.RelayPool
	if len(encryptedRequestRelays) > 0 {
		encryptedRequestPool = nostrAdapter.NewRelayPool(encryptedRequestRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
		encryptedRequestPool.Connect(ctx)
	}

	relayURLs := interopRelayURLs(cfg, controlPlaneRelays)
	relayPool := nostrAdapter.NewRelayPool(relayURLs, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey))
	relayPool.Connect(ctx)
	logger.Info("nostr relay topology initialized",
		zap.Strings("control_plane_relays", controlPlaneRelays),
		zap.Strings("encrypted_request_relay_urls", encryptedRequestRelays),
		zap.Strings("interop_relays", relayURLs),
		zap.Bool("sidecar_enabled", cfg.Nostr.Sidecar.Enabled),
		zap.Bool("mirror_external", cfg.Nostr.Sidecar.MirrorExternal),
	)

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
	controlPlaneSigner, err := controlplane.NewPrivateKeySigner(cfg.Nostr.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("configuring control-plane signer: %w", err)
	}

	// Worker policy service for environment-specific worker selection.
	workerPolicySvc := service.NewWorkerPolicyService(workerRepo, logger)

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
			service.WithAdoptionTxExecutor(repository.NewPgTxExecutor(pool)),
		)
	}
	var runtimeLifecycleSvc *service.RuntimeLifecycleService
	if cfg.DirectRuntime.Enabled {
		runtimeLifecycleSvc = service.NewRuntimeLifecycleService(
			registry, serviceRepo, envRepo, artifactRepo, stateRepo, runtimeResolver, publisher, logger,
			service.WithRuntimeLifecycleSecrets(secretRepo, secretEncryptor),
		)
	}

	// Reconciler (created here but started in Run() with the lifecycle context).
	var rec *reconcile.Reconciler
	if cfg.Reconcile.Enabled {
		rec = reconcile.NewReconciler(
			serviceRepo, envRepo, artifactRepo, obsRepo, stateRepo,
			runtimeResolver, publisher, cfg.Reconcile.Interval, logger,
		)
	}

	// Telemetry.
	telemetryProvider := telemetry.Setup(telemetry.Config{
		Enabled:      cfg.Telemetry.Enabled,
		ServiceName:  cfg.Telemetry.ServiceName,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
	}, logger)

	// Background runner manager.
	bgManager := NewBackgroundManager(logger)

	// Generic AI/ML registry foundation. Bucket-B keeps this additive and does not
	// move long-running orchestration off the existing LLM path until later buckets.
	mlRegistryRepo := repository.NewPgMLRegistryRepository(pool)
	mlRegistry := service.NewMLRegistryService(mlRegistryRepo, publisher, logger, service.WithMLEnvironmentRepository(envRepo))

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
		placementSvc := service.NewLLMPlacementService(workerRepo, logger)
		coordOpts := []service.LLMProvisioningCoordinatorOption{
			service.WithLLMCoordinatorIntervals(cfg.LLM.CoordinatorPollInterval, cfg.LLM.StaleRunTimeout),
		}
		if controlPlaneSigner != nil && controlPlanePool != nil {
			llmResponder = controlplane.NewLLMResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo)
			coordOpts = append(coordOpts, service.WithLLMProvisioningResponder(llmResponder))
		}
		llmCoordinator := service.NewLLMProvisioningCoordinator(llmRegistry, envRepo, llmRunRepo, placementSvc, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, logger, coordOpts...)
		llmReconciler := reconcile.NewLLMRouteReconciler(llmRegistry, envRepo, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, cfg.LLM.ReconcileInterval, logger)
		bgManager.Register(llmCoordinator)
		bgManager.Register(llmReconciler)
		logger.Info("LLM control plane enabled", zap.String("default_gateway_ref", cfg.LLM.DefaultGatewayRef))
	}

	// Policy service.
	policySvc := service.NewPolicyService(policyRepo, sigRepo, sbomRepo, logger)

	// Package repository control plane.
	var packageProjection repository.PackageControlPlaneRepository
	var packageRegistrySvc *service.PackageRegistryService
	if cfg.Packages.Enabled {
		packageProjection = repository.NewPgPackageControlPlaneRepository(pool)
		packageBackends, err := packagefactory.BuildRegistry(cfg.Packages)
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
		nostrAdapter.WithMLProjectionSource(mlRegistry),
		nostrAdapter.WithWorkerProjectionSource(workerRepo),
		nostrAdapter.WithSystemDiscoveryConfig(cfg, true),
	}
	if llmRegistry != nil {
		projectorOpts = append(projectorOpts, nostrAdapter.WithLLMProjectionSource(llmRegistry))
	}
	nostrProjector := nostrAdapter.NewProjector(cfg.Nostr, registry, controlPlanePool, nostrEventRepo, logger, projectorOpts...)
	nostrProjector.SetupSubscriptions(publisher)
	if nostrProjector.Enabled() {
		bgManager.Register(nostrProjector)
		logger.Info("nostr read-model projector registered")
	}

	// Register the reconciler as a background runner (if enabled).
	if rec != nil {
		bgManager.Register(&reconcilerRunner{rec: rec})
	}

	// Nostr event processor: maps inbound events to domain commands.
	nostrProcessor := nostrAdapter.NewProcessor(registry, workerRepo, logger)

	// Nostr inbound subscriber: listens for Hive-CI, Loom, and Bahia events.
	nostrSub := nostrAdapter.NewSubscriber(relayPool, nostrEventRepo, logger,
		nostrAdapter.WithHandler(nostrProcessor.Handle),
		nostrAdapter.WithAuthorizedAuthors(controlPlaneAuthorizedPubkeys(cfg, service.AssistantIdentity{})),
	)
	bgManager.Register(nostrSub)

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
		bgManager.Register(NewOCIUploadCleanupRunner(ociSvc, cfg.OCI.UploadExpiry, logger))
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
		bgManager.Register(hiveSub)
		bgManager.Register(NewHiveCIRetryRunner(hiveRepo, bridge, cfg.HiveCI.RetryInterval, cfg.HiveCI.MaxRetries, logger))
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
	bgManager.Register(toolCoordinator)

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
	mcpServer := mcp.NewServerWithOptions(registry, logger, mcp.ServerDeps{
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
		PackageProjection:       packageProjection,
	})
	mcpHandler := handlers.NewMCPHandler(mcpServer, logger)
	logger.Info("mcp server initialized")

	var assistantOrchestrator *service.AssistantOrchestrator
	var assistantIdentity service.AssistantIdentity
	if cfg.Assistant.Enabled {
		identity := bootstrapOperatorAssistant(ctx, cfg, controlPlaneRelays, logger)
		assistantIdentity = identity
		contextBuilder := service.NewAssistantContextBuilder(registry, llmRegistry, mlRegistry, nil, service.AssistantContextBuilderConfig{})
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
		bgManager.Register(service.NewAssistantSessionRecoveryRunner(assistantOrchestrator, service.AssistantSessionRecoveryConfig{RecentLimit: 500, ServicePubkey: servicePubkey, Logger: slog.Default()}))
		logger.Info("operator assistant orchestrator initialized", zap.String("agent_id", identity.AgentID), zap.String("assistant_pubkey", identity.Pubkey))
	}

	// Encrypted request/result event runtime for sensitive browser route migrations.
	if len(encryptedRequestRelays) > 0 && encryptedRequestPool != nil && controlPlaneSigner != nil && cfg.Nostr.PrivateKey != "" {
		responder := controlplane.NewEncryptedResponder(encryptedRequestPool, controlPlaneSigner, cfg.Nostr.PrivateKey, logger)
		encryptedRequestTransport := controlplane.NewEncryptedRequestTransport(encryptedRequestPool, responder, cfg.Nostr.AuthorizedPubkeys, logger)
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
		bgManager.Register(&encryptedRequestTransportRunner{transport: encryptedRequestTransport})
		logger.Info("encrypted request/result event runtime registered", zap.Strings("relay_urls_for_encrypted_nostr_requests", encryptedRequestRelays))
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
		reactorOpts := []controlplane.ReactorOption{
			controlplane.WithAdoptionService(adoptionSvc),
			controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc),
			controlplane.WithToolProvisioningRepository(toolProvisionRepo),
			controlplane.WithToolResponder(controlplane.NewToolResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo)),
			controlplane.WithToolProvisioningCoordinator(toolCoordinator),
			controlplane.WithPolicyService(policySvc),
			controlplane.WithMLRegistry(mlRegistry),
		}
		if llmRegistry != nil {
			reactorOpts = append(reactorOpts, controlplane.WithLLMRegistry(llmRegistry))
		}
		if assistantOrchestrator != nil {
			reactorOpts = append(reactorOpts, controlplane.WithAssistantOrchestrator(assistantOrchestrator))
		}
		if packageRegistrySvc != nil {
			reactorOpts = append(reactorOpts,
				controlplane.WithPackageRegistryService(packageRegistrySvc),
				controlplane.WithPackageProjectionRepository(packageProjection),
				controlplane.WithNostrEventRepository(nostrEventRepo),
			)
		}
		reactor := controlplane.NewReactor(reactorConfig, registry, controlPlanePool, controlPlaneSigner, logger, reactorOpts...)
		bgManager.Register(&controlplaneRunner{reactor: reactor})
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
			Config:           cfg,
			AuthMiddleware:   authMiddleware,
			Workers:          workerRepo,
			Builds:           buildRepo,
			Runs:             runRepo,
			Services:         serviceRepo,
			Environments:     envRepo,
			EnvStates:        stateRepo,
			RuntimeResolver:  runtimeResolver,
			Payments:         paymentSvc,
			SBOMs:            sbomRepo,
			Artifacts:        artifactRepo,
			Policies:         policySvc,
			Adoption:         adoptionSvc,
			RuntimeLifecycle: runtimeLifecycleSvc,
			Secrets:          secretRepo,
			Encryptor:        secretEncryptor,
			Notifications:    notifRepo,
			Dispatcher:       notifDispatcher,
			MCP:              mcpHandler,
			Blossom:          blossomClient,
			OCI:              ociHandler,
			Orgs:             orgRepo,
			OrgMembers:       orgMemberRepo,
			OrgInvites:       orgInviteRepo,
			RBAC:             rbac,
			MLRegistry:       mlRegistry,
			MLCommands:       mlCommandPublisher,
			LLMRegistry:      llmRegistry,
		}, cfg.Auth)

	httpServer := &http.Server{
		Addr:         cfg.ServerAddress(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		DB:              pool,
		Registry:        registry,
		MLRegistry:      mlRegistry,
		LLMRegistry:     llmRegistry,
		HTTPServer:      httpServer,
		Publisher:       publisher,
		Coordinator:     coord,
		Reconciler:      rec,
		NostrPub:        nostrPub,
		Telemetry:       telemetryProvider,
		Background:      bgManager,
		toolCoordinator: toolCoordinator,
		relayPools:      []*nostrAdapter.RelayPool{controlPlanePool, relayPool, encryptedRequestPool},
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
	closeRelayPools(a.relayPools...)

	a.DB.Close()
	_ = a.Logger.Sync()
	a.Logger.Info("server stopped gracefully")
	return nil
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

func encryptedRequestRelayURLs(cfg config.NostrConfig) []string {
	return append([]string(nil), cfg.EncryptedRequestRelays...)
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
	for _, r := range cfg.Nostr.EncryptedRequestRelays {
		relays = appendUniqueRelay(relays, r)
	}
	for _, r := range cfg.Loom.Relays {
		relays = appendUniqueRelay(relays, r)
	}
	return relays
}

func llmGatewayHTTPConfig(cfg config.LLMControlplaneConfig) llmadapter.GatewayHTTPConfig {
	endpoints := make(map[string]llmadapter.GatewayHTTPEndpointConfig, len(cfg.Gateways))
	for ref, ep := range cfg.Gateways {
		endpoints[ref] = llmadapter.GatewayHTTPEndpointConfig{Type: ep.Type, BaseURL: ep.BaseURL, AuthToken: ep.AuthToken, Timeout: ep.Timeout}
	}
	return llmadapter.GatewayHTTPConfig{Endpoints: endpoints}
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
