// Package app wires together all components and manages the application lifecycle.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	backupAdapter "github.com/openagentsinc/bahia/internal/adapters/backup"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	dnsAdapter "github.com/openagentsinc/bahia/internal/adapters/dns"
	giteaAdapter "github.com/openagentsinc/bahia/internal/adapters/gitea"
	"github.com/openagentsinc/bahia/internal/adapters/harbor"
	hiveciAdapter "github.com/openagentsinc/bahia/internal/adapters/hiveci"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/adapters/mcpclient"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin"
	registryAdapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	routingAdapter "github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	sbomAdapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	securityAdapter "github.com/openagentsinc/bahia/internal/adapters/security"
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
	"github.com/openagentsinc/bahia/internal/docs"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/pipeline"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/reconcile"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory"
	"github.com/openagentsinc/bahia/internal/workflow"
	"go.uber.org/zap"
)

// App holds all application components.
type App struct {
	Config                    *config.Config
	Logger                    *zap.Logger
	DB                        *pgxpool.Pool
	Registry                  *service.RegistryService
	MLRegistry                *service.MLRegistryService
	LLMRegistry               *service.LLMRegistryService
	HTTPServer                *http.Server
	Publisher                 events.Publisher
	Coordinator               *workflow.Coordinator
	Reconciler                *reconcile.Reconciler
	ManagedInstanceSupervisor *service.ManagedInstanceSupervisor
	NostrPub                  *nostrAdapter.Publisher
	Telemetry                 *telemetry.Provider
	Background                *BackgroundManager
	toolCoordinator           *service.ToolProvisioningCoordinator
	relayPools                []*nostrAdapter.RelayPool
	dnsBackendClosers         []io.Closer
	Health                    *HealthProvider
	RelayFirstRegistry        *service.RelayFirstRegistry
	SoulFactory               *soulfactory.Reactor
	soulFactoryCloser         func() error
	hiveCIInitiator           *giteaAdapter.Initiator
	localEventStore           *localstore.Store
	localOutbox               *localstore.Outbox
	reloadMu                  sync.Mutex

	// Phase 3 intent framework (F1).
	TrustSet            *controlplane.TrustSet
	IntentProcessor     *controlplane.IntentProcessor
	IntentReadiness     *controlplane.ReadinessTracker
	IntentSubscriber    *controlplane.IntentSubscriber
	IntentAuthorsSyncer *controlplane.IntentAuthorsSyncer
}

var (
	dbConnect = db.Connect
	dbMigrate = db.Migrate
)

func newSBOMGeneratorRegistry(cfg config.SBOMConfig) (*sbomAdapter.GeneratorRegistry, error) {
	var cdxgen sbomAdapter.Generator
	if cfg.Cdxgen.Enabled {
		cdxgen = sbomAdapter.NewCdxgenGenerator(sbomAdapter.CdxgenConfig{
			Enabled:    true,
			BinaryPath: cfg.Cdxgen.BinaryPath,
		})
	}
	return sbomAdapter.NewGeneratorRegistry(sbomAdapter.NewSyftGenerator(), cdxgen)
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
	zap.ReplaceGlobals(logger)

	ctx := context.Background()
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
	contextVMRequestRelays := contextVMRelayURLs(cfg.Nostr)
	contextVMResponseRelays := append([]string(nil), contextVMRequestRelays...)
	controlPlanePool := nostrAdapter.NewRelayPool(controlPlaneRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
	controlPlanePool.Connect(ctx)
	contextVMRequestPool := nostrAdapter.NewRelayPool(contextVMRequestRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
	contextVMRequestPool.Connect(ctx)
	contextVMResponsePool := nostrAdapter.NewRelayPool(contextVMResponseRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
	contextVMResponsePool.Connect(ctx)
	relayPolicyHydrationRelays := relayPolicyHydrationRelayURLs(cfg.Nostr)
	relayPolicyHydrationPool := nostrAdapter.NewRelayPool(relayPolicyHydrationRelays, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
	relayPolicyHydrationPool.Connect(ctx)

	relayURLs := interopRelayURLs(cfg, controlPlaneRelays)
	relayPool := nostrAdapter.NewRelayPool(relayURLs, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
	relayPool.Connect(ctx)
	logger.Info("nostr relay topology initialized",
		zap.Strings("control_plane_relays", controlPlaneRelays),
		zap.Strings("contextvm_request_subscription_relays", contextVMRequestRelays),
		zap.Strings("contextvm_response_publication_relays", contextVMResponseRelays),
		zap.Strings("interop_relays", relayURLs),
		zap.Bool("sidecar_enabled", cfg.Nostr.Sidecar.Enabled),
		zap.Bool("mirror_external", cfg.Nostr.Sidecar.MirrorExternal),
	)

	// Database cache is optional. When unavailable, keep tier0/tier1 relay-first
	// startup alive and use in-memory event audit/cursor storage.
	pool, dbAvailable := connectOptionalDatabase(ctx, cfg, logger)

	// Repositories. When DB is unavailable, PG-backed repositories are nil.
	// When Postgres is unavailable all DB-backed repositories are nil.
	// never exceed that cap, so route gating keeps tier2/tier3 routes (and
	// their nil repos) unreachable. RequireRepo gates return 503 for nil repos.
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
	var f74aSBOMBackfill service.F74aSBOMBackfillSource
	var sbomManifestRepo repository.SBOMManifestRepository
	var securityRepo repository.SecurityRepository
	var sigRepo repository.ArtifactSignatureRepository
	var policyRepo repository.DeploymentPolicyRepository
	var secretRepo repository.SecretRepository
	var hiveCIInitiator *giteaAdapter.Initiator
	var deploymentUnitRepo repository.DeploymentUnitRepository
	var orgRepo repository.OrganizationRepository
	var orgMemberRepo repository.OrgMemberRepository
	var orgInviteRepo repository.OrgInviteRepository
	var relayPolicyProjectionRepo repository.RelayPolicyProjectionRepository
	var dnsZoneRepo repository.DNSZoneRepository
	var dnsPolicyRepo repository.DNSPolicyRepository
	var dnsRecordOverrideRepo repository.DNSRecordOverrideRepository
	var dnsEndpointRepo repository.DNSEndpointRepository
	var dnsBackendRepo repository.DNSBackendRepository
	var contextVMResponseStore repository.ContextVMResponseStore
	var managedInstanceHealthRepo repository.ManagedInstanceHealthRepository
	var agentRuntimeReleaseRepo repository.AgentRuntimeReleaseRepository
	var virtualizationRepo repository.VirtualizationRepository

	if dbAvailable {
		virtualizationRepo = repository.NewPgVirtualizationRepository(pool)
		serviceRepo = repository.NewPgServiceRepository(pool)
		agentRuntimeReleaseRepo = repository.NewPgAgentRuntimeReleaseRepository(pool)
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
		pgSBOMRepo := repository.NewPgSBOMRepository(pool)
		sbomRepo = pgSBOMRepo
		f74aSBOMBackfill = pgSBOMRepo
		sbomManifestRepo = pgSBOMRepo
		securityRepo = repository.NewPgSecurityRepository(pool)
		sigRepo = repository.NewPgArtifactSignatureRepository(pool)
		policyRepo = repository.NewPgDeploymentPolicyRepository(pool)
		secretRepo = repository.NewPgSecretRepository(pool)
		deploymentUnitRepo = repository.NewPgDeploymentUnitRepository(pool)
		managedInstanceHealthRepo = repository.NewPgManagedInstanceHealthRepository(pool)
		orgRepo = repository.NewPgOrganizationRepository(pool)
		orgMemberRepo = repository.NewPgOrgMemberRepository(pool)
		orgInviteRepo = repository.NewPgOrgInviteRepository(pool)
		dnsZoneRepo = repository.NewPgDNSZoneRepository(pool)
		dnsPolicyRepo = repository.NewPgDNSPolicyRepository(pool)
		dnsRecordOverrideRepo = repository.NewPgDNSRecordOverrideRepository(pool)
		contextVMResponseStore = repository.NewPgContextVMResponseStore(pool)
		if pool != nil {
			relayPolicyProjectionRepo = repository.NewPgRelayPolicyProjectionRepository(pool)
		}
	} else {
		logger.Warn("database unavailable: tier2/tier3 repositories are nil, route gating will return 503 for those tiers")
	}
	tenantRBAC := newTenantRBAC(orgMemberRepo)

	// Local event store and publish outbox (bahia-irsry.10.1, .10.4). They are
	// present at every tier: the store is the inbound subscriptions' cache,
	// dedup set and per-(relay, filter) cursors, and the daemon's own outputs;
	// the outbox holds every event the daemon publishes until its relays
	// accept it. Neither needs PostgreSQL.
	localEventStore, err := localstore.Open(cfg.Nostr.LocalStore.Path)
	if err != nil {
		return nil, fmt.Errorf("opening local Nostr event store: %w", err)
	}
	localOutbox, err := localstore.OpenOutbox(cfg.Nostr.LocalStore.ResolvedOutboxPath())
	if err != nil {
		_ = localEventStore.Close()
		return nil, fmt.Errorf("opening local Nostr publish outbox: %w", err)
	}
	if aside := localOutbox.MovedAside(); aside != "" {
		logger.Error("local Nostr publish outbox was unreadable and was moved aside; events still pending in it were not delivered",
			zap.String("moved_to", aside))
	}
	if err := repository.BootstrapLocalDNS(ctx, localOutbox, dnsZoneRepo, dnsPolicyRepo, dnsRecordOverrideRepo); err != nil {
		_ = localOutbox.Close()
		_ = localEventStore.Close()
		return nil, fmt.Errorf("bootstrapping local DNS registry: %w", err)
	}
	// DNS and ML desired registry records live in the durable local store. The
	// optional PostgreSQL repositories only seed pre-migration data; they are
	// not a write prerequisite for these mutations.
	dnsZoneRepo = repository.NewLocalDNSZoneRepository(localOutbox)
	dnsPolicyRepo = repository.NewLocalDNSPolicyRepository(localOutbox)
	dnsRecordOverrideRepo = repository.NewLocalDNSRecordOverrideRepository(localOutbox)
	dnsEndpointRepo = repository.NewLocalDNSEndpointRepository(localOutbox)
	dnsBackendRepo = repository.NewLocalDNSBackendRepository(localOutbox)
	localNostrReleased := false
	defer func() {
		if !localNostrReleased {
			_ = localOutbox.Close()
			_ = localEventStore.Close()
		}
	}()

	// PostgreSQL nostr_events, when available: an archive and index for the
	// PostgreSQL-backed readers, and the outbox of producers that write their
	// audit event in the same transaction as the change it audits. Relay
	// delivery and inbound idempotency never depend on it.
	var pgNostrEventRepo repository.NostrEventRepository
	if dbAvailable {
		pgNostrEventRepo = repository.NewPgNostrEventRepository(pool)
	}

	loomClientOptions := []loom.ClientOption{loom.WithWorkerRepo(workerRepo)}
	loomCanonicalSigner, loomSignetManager, err := newLoomCanonicalProjectionSigner(cfg, relayURLs, logger)
	if err != nil {
		return nil, fmt.Errorf("configuring Loom canonical projection signer: %w", err)
	}
	if loomCanonicalSigner != nil {
		loomClientOptions = append(loomClientOptions, loom.WithCanonicalSigner(loomCanonicalSigner))
	}
	loomClient := loom.NewClient(cfg.Loom, cfg.Nostr.PrivateKey, relayPool, logger, loomClientOptions...)

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

	// Policy service gates both artifact and runtime-release deployment intents.
	policySvc := service.NewPolicyService(policyRepo, sigRepo, sbomRepo, logger, service.WithSecurityRepository(securityRepo))

	// Registry service.
	registryOptions := []service.RegistryOption{
		service.WithDeploymentApprovalPolicy(policySvc),
		service.WithManualArtifactRegistration(cfg.HiveCI.AllowManualArtifactRegistration),
		service.WithLiveArtifactImport(cfg.HiveCI.AllowLiveArtifactImport),
	}
	if dbAvailable && pool != nil {
		registryOptions = append(registryOptions, service.WithRegistryTxExecutor(repository.NewPgTxExecutor(pool)))
	}
	if agentRuntimeReleaseRepo != nil {
		registryOptions = append(registryOptions, service.WithAgentRuntimeReleaseRepository(agentRuntimeReleaseRepo))
	}
	registry := service.NewRegistryService(
		serviceRepo, envRepo, buildRepo, artifactRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, publisher, logger,
		registryOptions...,
	)
	var agentRuntimeReleaseSvc *service.AgentRuntimeReleaseService
	if agentRuntimeReleaseRepo != nil && serviceRepo != nil {
		agentRuntimeReleaseSvc = service.NewAgentRuntimeReleaseService(agentRuntimeReleaseRepo, serviceRepo)
	}
	nostrPub := nostrAdapter.NewPublisher(cfg.Nostr, relayPool, pgNostrEventRepo, logger,
		nostrAdapter.WithLocalOutbox(localOutbox, localEventStore))
	// Control-plane outbox publisher shared by the read-model projector, docs,
	// SBOM and config-fabric. Its entries carry the control-plane publish
	// target and its own runner retries them, so they are never redelivered to
	// the interop relays.
	controlPlanePub := nostrAdapter.NewPublisher(cfg.Nostr, controlPlanePool, pgNostrEventRepo, logger,
		nostrAdapter.WithPublishTarget(repository.NostrPublishTargetControlPlane),
		nostrAdapter.WithLocalOutbox(localOutbox, localEventStore))
	// nostr_events for its readers: PostgreSQL when available, else the
	// local event store (B-12). An event a producer records there as pending
	// delivery goes to the outbox of its publish target.
	localOutboxAdmit := func(ctx context.Context, ev nostr.Event, target, entityType string, entityID *uuid.UUID) error {
		switch target {
		case repository.NostrPublishTargetDefault:
			return nostrPub.Enqueue(ctx, ev, entityType, entityID)
		case repository.NostrPublishTargetControlPlane:
			return controlPlanePub.Enqueue(ctx, ev, entityType, entityID)
		default:
			return fmt.Errorf("unknown publish target %q", target)
		}
	}
	nostrEventRepo := pgNostrEventRepo
	if nostrEventRepo == nil {
		nostrEventRepo = nostrAdapter.NewLocalEventRepository(localEventStore, localOutboxAdmit)
	}
	// Audit writers always use the local event store backed by the local
	// outbox, even when PostgreSQL is available (bahia-irsry.62). This
	// decouples audit publishing from PostgreSQL and makes the drain loop
	// unnecessary; the publisher archives the outcome to PostgreSQL for
	// its readers, best effort.
	auditEventRepo := nostrAdapter.NewLocalEventRepository(localEventStore, localOutboxAdmit)

	controlPlaneSigner, err := controlplane.NewPrivateKeySigner(cfg.Nostr.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("configuring control-plane signer: %w", err)
	}
	hiveCIJobClient := loom.NewClient(
		cfg.Loom, cfg.Nostr.PrivateKey, controlPlanePool, logger,
		loom.WithWorkerRepo(workerRepo), loom.WithJobSigner(controlPlaneSigner),
	)
	var continuityProjectionPublisher service.ContinuityNostrPublishFunc
	if controlPlanePool != nil && controlPlaneSigner != nil {
		continuityProjectionPublisher = func(ctx context.Context, kind int, tags nostr.Tags, content string) error {
			ev := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: content}
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
	workerCleanupStatePublisher := controlplane.NewWorkerCleanupStatePublisher(controlPlanePool, controlPlaneSigner)
	workerCleanupStatePublisher.ConfigureAudit(nostrEventRepo, logger)

	// Worker policy service for environment-specific worker selection.
	workerPolicySvc := service.NewWorkerPolicyService(workerRepo, logger, service.WithWorkerPolicyPressureThresholds(pressureThresholds))

	// Runtime resolver — selects Docker, Compose, or Kubernetes per service/environment.
	runtimeRegistryAuth := runtimeRegistryAuth(cfg)
	runtimeResolver := runtime.NewConfigRuntimeResolver(cfg.Runtime, logger, runtimeRegistryAuth)
	logger.Info("runtime resolver initialized", zap.String("default_type", cfg.Runtime.Type))

	// Secret encryptor (uses Bahia's Nostr key for at-rest encryption).
	var secretEncryptor *secretsAdapter.Encryptor
	if cfg.Nostr.PrivateKey != "" {
		secretEncryptor, err = secretsAdapter.NewEncryptor(cfg.Nostr.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("configuring secret encryption: %w", err)
		}
		logger.Info("secrets encryption enabled")
	}

	var publicRoutePlanner *service.PublicRoutePlanner
	var internalRouteBackend *routingAdapter.NginxBackend
	if cfg.EdgeRouting.Enabled {
		publicRoutePlanner, internalRouteBackend, err = buildPublicRoutePlanner(ctx, cfg.EdgeRouting, cfg.InternalRouting, secretRepo, secretEncryptor, logger)
		if err != nil {
			return nil, fmt.Errorf("configuring edge routing: %w", err)
		}
		logger.Info("managed edge routing enabled", zap.String("provider", cfg.EdgeRouting.Provider), zap.String("backend_ref", cfg.EdgeRouting.BackendRef), zap.Bool("internal_https", internalRouteBackend != nil))
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
			service.WithAdoptionDeploymentUnits(deploymentUnitRepo),
			service.WithAdoptionTxExecutor(repository.NewPgTxExecutor(pool)),
		)
	}
	var runtimeApplyLock *service.RuntimeApplyLock
	if dbAvailable {
		runtimeApplyLock = service.NewRuntimeApplyLock(pool, logger)
	}
	var runtimeLifecycleSvc *service.RuntimeLifecycleService
	if cfg.DirectRuntime.Enabled {
		var runtimeApplyLockOpts []service.RuntimeLifecycleOption
		runtimeApplyLockOpts = append(runtimeApplyLockOpts, service.WithRuntimeLifecycleSecrets(secretRepo, secretEncryptor))
		runtimeApplyLockOpts = append(runtimeApplyLockOpts, service.WithRuntimeLifecycleDeploymentUnits(deploymentUnitRepo))
		if runtimeApplyLock != nil {
			runtimeApplyLockOpts = append(runtimeApplyLockOpts, service.WithRuntimeApplyLock(runtimeApplyLock))
		}
		runtimeLifecycleSvc = service.NewRuntimeLifecycleService(
			registry, serviceRepo, envRepo, artifactRepo, stateRepo, runtimeResolver, publisher, logger,
			runtimeApplyLockOpts...,
		)
	}

	// Workflow coordinator. Deployment-unit routing is wired even when direct
	// runtime actions are disabled so Compose-targeted intents fail closed
	// instead of falling back to Loom's bare container deploy.
	coordinatorOptions := []workflow.CoordinatorOption{
		workflow.WithWorkerPolicy(workerPolicySvc),
		workflow.WithDeploymentUnitRouting(deploymentUnitRepo, runtimeLifecycleSvc),
	}

	// Route canaries. Converging a routing provider only proves configuration
	// was accepted, not that the route serves traffic, so managed routes are
	// verified end to end and watched continuously.
	var routeCanarySupervisor *service.RouteCanarySupervisor
	var routeCanaryStore service.RouteCanaryRepository
	if publicRoutePlanner != nil && cfg.RouteCanaries.Enabled {
		if dbAvailable && pool != nil {
			pgRouteCanaries := repository.NewPgRouteCanaryRepository(pool)
			routeCanaryStore = pgRouteCanaries
		}
		routeCanaryEvaluator, evalErr := service.NewRouteCanaryEvaluator(runtime.RouteProber{}, cfg.RouteCanaries.Policy())
		if evalErr != nil {
			return nil, fmt.Errorf("configuring route canary evaluator: %w", evalErr)
		}
		var routeHealthSource service.RouteInstanceHealthSource
		if managedInstanceHealthRepo != nil {
			healthSource := service.NewManagedInstanceRouteHealthSource(managedInstanceHealthRepo)
			routeHealthSource = healthSource
		}
		canaryCfg := cfg.RouteCanaries.Normalized()

		// The gate decorates the planner rather than being spliced into the
		// coordinator, so the route-only and combined deploy paths are both
		// verified through the single apply call each already makes.
		if canaryCfg.GateEnabled {
			// The gate publishes its transitions on the same bus as the
			// supervisor, so a gate-opened or gate-recovered outage reaches the
			// Nostr projection and notifications instead of only the database.
			gate, gateErr := service.NewRouteCanaryGate(publicRoutePlanner, routeCanaryEvaluator, routeCanaryStore, routeHealthSource, publisher,
				service.RouteCanaryGateConfig{Timeout: canaryCfg.GateTimeout, RetryInterval: canaryCfg.GateRetryInterval}, logger)
			if gateErr != nil {
				return nil, fmt.Errorf("configuring route canary gate: %w", gateErr)
			}
			coordinatorOptions = append(coordinatorOptions, workflow.WithPublicRoutes(gate))
		} else {
			coordinatorOptions = append(coordinatorOptions, workflow.WithPublicRoutes(publicRoutePlanner))
		}

		// Periodic probing needs somewhere to record verdicts; without a
		// database there is no durable outage state to maintain.
		if routeCanaryStore != nil && stateRepo != nil {
			routeCanarySupervisor, err = service.NewRouteCanarySupervisor(
				service.NewDesiredStateRoutePlanSource(stateRepo),
				routeCanaryStore, routeCanaryEvaluator, routeHealthSource, publisher, canaryCfg.Interval, logger)
			if err != nil {
				return nil, fmt.Errorf("configuring route canary supervisor: %w", err)
			}
			// Project supervisor transitions to canonical Nostr observables so a
			// route outage is visible to Nostr consumers and fleet-health telemetry.
			if cfg.Nostr.PublishEnabled && strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
				if _, projErr := service.NewRouteCanaryProjector(publisher, nostrPub, logger); projErr != nil {
					return nil, fmt.Errorf("configuring route canary projector: %w", projErr)
				}
			}
		}
	} else if publicRoutePlanner != nil {
		coordinatorOptions = append(coordinatorOptions, workflow.WithPublicRoutes(publicRoutePlanner))
	}
	coord := workflow.NewCoordinator(registry, loomClient, publisher, logger, coordinatorOptions...)
	coord.SetupEventHandlers(publisher)
	if dbAvailable && pool != nil {
		if err := coord.RecoverNonTerminalRuns(ctx); err != nil {
			logger.Warn("failed to recover non-terminal deployment runs", zap.Error(err))
		}
	}

	// Reconciler (created here but started in Run() with the lifecycle context).
	var rec *reconcile.Reconciler
	if cfg.Reconcile.Enabled {
		reconcilerOpts := []reconcile.Option{
			reconcile.WithDeploymentHistory(intentRepo, runRepo),
		}
		if runtimeLifecycleSvc != nil {
			reconcilerOpts = append(reconcilerOpts, reconcile.WithAutoRemediationDeployer(runtimeLifecycleSvc))
		}
		rec = reconcile.NewReconciler(
			serviceRepo, envRepo, artifactRepo, deploymentUnitRepo, obsRepo, stateRepo,
			runtimeResolver, publisher, cfg.Reconcile.Interval, logger,
			reconcilerOpts...,
		)
	}

	var managedInstanceSupervisor *service.ManagedInstanceSupervisor
	if cfg.Supervision.Enabled && managedInstanceHealthRepo != nil && runtimeApplyLock != nil {
		configuredSpecs, specErr := configuredSupervisionSpecs(cfg.Supervision, logger)
		if specErr != nil {
			return nil, specErr
		}
		policy := defaultSupervisionPolicy(cfg.Supervision.ObserveOnly)
		source := &service.RepositorySupervisionSpecSource{Configured: configuredSpecs, States: stateRepo, Services: serviceRepo, Environments: envRepo, Units: deploymentUnitRepo, Resolver: runtimeResolver, Policy: policy, MemoryThreshold: cfg.Supervision.MemoryThreshold}
		managedInstanceSupervisor, err = service.NewManagedInstanceSupervisor(source, managedInstanceHealthRepo, runtimeApplyLock, publisher, cfg.Supervision.Interval, logger, cfg.Supervision.ObservationTimeout)
		if err != nil {
			return nil, fmt.Errorf("configuring managed instance supervisor: %w", err)
		}
		service.NewManagedInstanceHealthProjector(publisher, nostrPub, logger)
	}

	// Telemetry.
	telemetryProvider := telemetry.Setup(telemetry.Config{
		Enabled:      cfg.Telemetry.Enabled,
		ServiceName:  cfg.Telemetry.ServiceName,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
	}, logger)
	if dbAvailable {
		telemetryProvider.SetFleetHealthSources(workerRepo, stateRepo)
	}

	// One-shot migration: move any pre-upgrade pending PostgreSQL outbox rows
	// into the local outbox so they are delivered by the local runner. After
	// this, no PostgreSQL drain loop runs (bahia-irsry.62).
	if pool != nil {
		for _, pub := range []*nostrAdapter.Publisher{nostrPub, controlPlanePub} {
			if n, err := pub.MigratePendingPostgresRows(ctx); err != nil {
				logger.Error("migrate pending PostgreSQL outbox rows", zap.String("target", pub.Target()), zap.Error(err))
			} else if n > 0 {
				logger.Info("migrated pending PostgreSQL outbox rows to local outbox",
					zap.String("target", pub.Target()), zap.Int("count", n))
			}
		}
	}

	// Background runner manager and startup health provider.
	bgManager := NewBackgroundManager(logger)
	bgManager.RegisterWithOptions(nostrPub)
	bgManager.RegisterWithOptions(controlPlanePub)
	if managedInstanceSupervisor != nil {
		bgManager.RegisterWithOptions(managedInstanceSupervisor, RunnerRequired(false))
	}
	if routeCanarySupervisor != nil {
		bgManager.RegisterWithOptions(routeCanarySupervisor, RunnerRequired(false))
	}
	if loomSignetManager != nil {
		bgManager.RegisterWithOptions(loomSignetManager, RunnerRequired(false))
	}
	if securityRepo != nil {
		bgManager.RegisterWithOptions(NewOSVVulnerabilityCacheCleanupRunner(securityRepo, defaultOSVVulnerabilityCacheCleanupInterval, logger))
	}
	if contextVMResponseStore != nil {
		bgManager.RegisterWithOptions(NewContextVMResponseCleanupRunner(contextVMResponseStore, defaultContextVMResponseRetention, time.Hour, logger), RunnerRequired(false))
	}
	if cfg.Nostr.PublishEnabled && strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
		if staleRunSource, ok := runRepo.(workflow.DeploymentRunHealthSource); ok {
			bgManager.RegisterWithOptions(
				workflow.NewStaleRunDetector(staleRunSource, nostrEventRepo, nostrPub, cfg.Nostr.StaleRunAfter, logger),
				RunnerRequired(false),
			)
		}
	}
	healthProvider := NewHealthProvider(nil, bgManager)
	healthProvider.SetRelayQuorumConfig(RelayQuorumConfig{
		FullMinHealthy:      cfg.Nostr.RelayQuorum.FullMinHealthy,
		DegradedMinHealthy:  cfg.Nostr.RelayQuorum.DegradedMinHealthy,
		EmergencyMinHealthy: cfg.Nostr.RelayQuorum.EmergencyMinHealthy,
	})
	healthProvider.SetRelayHealthFunc(func() (connected, healthy int) {
		return aggregateRelayHealth(controlPlanePool, relayPool)
	})
	registerSignetHealthCheck(healthProvider, loomSignetManager)
	if internalRouteBackend != nil {
		healthProvider.RegisterCheck("internal_routing", func() HealthCheck {
			check := HealthCheck{Name: "internal_routing", Status: HealthStatusPass, Message: "nginx include directory and certificate files are ready"}
			if err := internalRouteBackend.HealthCheck(context.Background()); err != nil {
				check.Status = HealthStatusFail
				check.Message = err.Error()
			}
			return check
		})
	}
	if !dbAvailable {
		bgManager.RegisterWithOptions(newDatabaseRecoveryRunner(cfg.DB, 30*time.Second, logger), RunnerRequired(false))
	}

	var soulFactoryRuntime *soulFactoryRuntime
	soulFactoryRuntime, err = buildSoulFactoryRuntime(ctx, cfg, registry, agentRuntimeReleaseSvc, deploymentUnitRepo, logger)
	if err != nil {
		return nil, fmt.Errorf("configuring SoulFactory OpenClaw runtime: %w", err)
	}
	soulFactoryRuntimeReleased := false
	defer func() {
		if !soulFactoryRuntimeReleased && soulFactoryRuntime != nil && soulFactoryRuntime.close != nil {
			_ = soulFactoryRuntime.close()
		}
	}()
	if soulFactoryRuntime != nil {
		telemetryProvider.SetOpenClawSagaExporter(soulFactoryRuntime.sagaMonitor.WritePrometheus)
		bgManager.RegisterWithOptions(soulFactoryRuntime.connection, RunnerRequired(false))
		registerSignetHealthCheck(healthProvider, soulFactoryRuntime.connection)
		bgManager.RegisterWithOptions(soulFactoryRuntime.runner)
		logger.Info("SoulFactory reactor registered", zap.Bool("enabled", cfg.SoulFactory.Enabled))
	}

	catalog := nostrAdapter.NewKindCatalog()

	// Relay projection cache: applies decoded relay events to local repositories.
	// When DB is unavailable, appliers are skipped (tier1-only mode has no
	// projection cache). Uses in-memory meta repo so bootstrap can track
	// ordering without requiring Postgres.
	var bootstrapCache nostrAdapter.BootstrapCacheApplier
	if dbAvailable {
		projectionMetaRepo := newInMemoryProjectionMetaRepo()
		projectionCache := service.NewRelayProjectionCache(projectionMetaRepo, logger)
		projectionCache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{
			Workers:      workerRepo,
			Services:     serviceRepo,
			Environments: envRepo,
			Builds:       buildRepo,
			Artifacts:    artifactRepo,
			Policies:     policyRepo,
		})
		bootstrapCache = &bootstrapCacheAdapter{cache: projectionCache}
		// Tombstones cached projections when their NIP-40 expiration passes.
		bgManager.RegisterWithOptions(projectionCache, RunnerRequired(false))
	}

	// Bahia self-identity publisher: emits 31410/31411/30360 events to relays.
	// Wired but gated behind nostrPub availability (requires relay connectivity).
	var bahiaStatusProjector *service.BahiaStatusProjector
	if nostrPub != nil {
		bahiaStatusProjector = service.NewBahiaStatusProjector(nostrPub, logger, cfg.Nostr.PrivateKey)
	}

	servicePubkey := ""
	if strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
		if secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey)); err == nil {
			servicePubkey = secret.Public().Hex()
		}
	}
	controlPlaneAuthors := compactBootstrapAuthors([]string{servicePubkey}, cfg.Nostr.AuthorizedPubkeys, cfg.Auth.BootstrapOwnerPubkeys)
	bootstrapper := nostrAdapter.NewBootstrapper(relayPool, catalog, localEventStore, bootstrapCache, logger, nostrAdapter.BootstrapConfig{
		ProjectionAuthors:   compactBootstrapAuthors([]string{servicePubkey}),
		ControlPlaneAuthors: controlPlaneAuthors,
		SelfAuthors:         compactBootstrapAuthors([]string{servicePubkey}),
		Resume:              inboundSyncConfigScoped(cfg.Nostr.LocalStore, cfg.Nostr.ServiceRelays),
	})
	healthProvider.SetBootstrapFunc(func() (phase string, ready bool) {
		progress := bootstrapper.Progress()
		return string(progress.Phase), bootstrapper.Ready()
	})
	healthProvider.SetBootstrapDetailsFunc(func() map[string]string {
		progress := bootstrapper.Progress()
		details := map[string]string{}
		if progress.CurrentGroup != "" {
			details["current_group"] = progress.CurrentGroup
		}
		if len(progress.BlockingRelays) > 0 {
			details["blocking_relays"] = strings.Join(progress.BlockingRelays, ",")
		}
		if progress.LastError != "" {
			details["last_error"] = progress.LastError
		}
		return details
	})
	// Phase 3 intent framework (F1): TrustSet, IntentProcessor, ReadinessTracker.
	// These are wired unconditionally; domain handlers register at startup when
	// their domain is not listed in nostr.intent_domains_disabled.
	trustSetOpts := []controlplane.TrustSetOption{
		controlplane.WithBootstrapOwners(cfg.Nostr.BootstrapOwners),
	}
	if tenantRBAC != nil {
		trustSetOpts = append(trustSetOpts, controlplane.WithPostgresRBAC(tenantRBAC))
	}
	trustSet := controlplane.NewTrustSet(cfg.Nostr.AuthorizedPubkeys, logger, trustSetOpts...)
	intentReadiness := controlplane.NewReadinessTracker()
	healthProvider.SetReadinessTracker(intentReadiness)
	enabledDomains := controlplane.BuildEnabledDomains(cfg.Nostr.IntentDomainsDisabled, cfg.Nostr.IntentDomains)
	if len(enabledDomains) > 0 {
		intentReadiness.RegisterFilter("intent-30900")
	}
	var intentStatus *controlplane.IntentStatusPublisher
	if nostrPub != nil && controlPlaneSigner != nil {
		intentStatus = controlplane.NewIntentStatusPublisher(
			func(ctx context.Context, ev nostr.Event) error {
				return nostrPub.PublishBeforeCommit(ctx, ev, "intent-status", nil)
			},
			controlPlaneSigner,
			logger,
		)
	}
	intentProcessor := controlplane.NewIntentProcessor(
		trustSet, localEventStore, intentStatus,
		controlplane.IntentProcessorConfig{EnabledDomains: enabledDomains},
		logger,
	)

	// Readiness tracked by HealthProvider via ReadinessTracker (§6.2).

	// Phase 3 intent subscriber (F1): runs when intent domains are enabled.
	// Author-scoped subscription via TrustSet, feeding the processor, marking
	// readiness after first catch-up.
	var intentSubscriber *controlplane.IntentSubscriber
	if len(enabledDomains) > 0 {
		intentSubscriber = controlplane.NewIntentSubscriber(
			controlPlanePool,
			localEventStore,
			trustSet,
			intentProcessor,
			intentReadiness,
			servicePubkey,
			logger,
		)
		bgManager.RegisterWithOptions(intentSubscriber, RunnerRequired(false))
	}

	// Phase 3 intent authors syncer (F1 §7.1): on startup and whenever the
	// TrustSet changes, push the current set of intent-permitted pubkeys to
	// each Bahia-owned sidecar via NIP-86 setintentauthors.
	var intentAuthorsSyncer *controlplane.IntentAuthorsSyncer
	if len(enabledDomains) > 0 {
		intentAuthorsSyncer = buildIntentAuthorsSyncer(ctx, cfg, trustSet, secretRepo, secretEncryptor, logger)
		if intentAuthorsSyncer != nil {
			bgManager.RegisterWithOptions(intentAuthorsSyncer, RunnerRequired(false))
			healthProvider.RegisterCheck("intent_authors_sync", func() HealthCheck {
				status := HealthStatusPass
				if intentAuthorsSyncer.SyncStatus().OutOfSync {
					status = HealthStatusWarn
				}
				return HealthCheck{Name: "intent_authors_sync", Status: status}
			})
			// Wrap the org member repo so Postgres membership mutations
			// propagate to the sidecar's intent authors set in real time.
			if orgMemberRepo != nil {
				orgMemberRepo = controlplane.NewNotifyingOrgMemberRepository(orgMemberRepo, intentAuthorsSyncer)
			}
		}
	}

	// The legacy nostr_events migration (internal/nostrmigration) is not on
	// the startup path (B-28): operators run it once with
	// `bahia-migrate nostr` (see docs/user-guide/cli-reference.md).
	bgManager.RegisterWithOptions(&bootstrapperRunner{
		bootstrapper:    bootstrapper,
		statusProjector: bahiaStatusProjector,
		catalogVersion:  catalog.Version,
		logger:          logger,
	}, RunnerRequired(false))

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
	bgManager.RegisterWithOptions(&failoverTriggerRunner{engine: continuityFailoverTrigger})

	var fipsRelayPool *nostrAdapter.RelayPool
	if cfg.FIPS.Enabled {
		fipsRelayPool = nostrAdapter.NewRelayPool(cfg.FIPS.RelayURLs, logger, nostrAdapter.WithPrivateKey(cfg.Nostr.PrivateKey), closedRetryBudgetOption(cfg.Nostr))
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
		bgManager.RegisterWithOptions(&fipsSubscriberRunner{subscriber: fipsSubscriber})
		logger.Info("FIPS overlay advert subscriber registered", zap.Strings("relay_urls", cfg.FIPS.RelayURLs), zap.String("app_namespace", cfg.FIPS.AppNamespace))
	}

	backupRegistryRepo := repository.NewPgBackupControlPlaneRepository(pool)
	backupRegistry := service.NewBackupRegistryService(backupRegistryRepo, publisher, logger)
	backupResponder := controlplane.NewBackupRunResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupRestoreResponder := controlplane.NewBackupRestoreResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupRetentionResponder := controlplane.NewBackupRetentionResponder(controlPlanePool, controlPlaneSigner, backupRegistry, nostrEventRepo, logger)
	backupResolver, err := service.NewStaticBackupBackendResolver(backupAdapter.NewKopiaBackend(), backupAdapter.NewVeleroBackend(), backupAdapter.NewPgBackend(), backupAdapter.NewQdrantBackend())
	if err != nil {
		return nil, fmt.Errorf("configuring backup backend resolver: %w", err)
	}
	backupRunOptions := []service.BackupRunCoordinatorOption{service.WithBackupRunResponder(backupResponder)}
	backupRestoreOptions := []service.BackupRestoreCoordinatorOption{service.WithBackupRestoreResponder(backupRestoreResponder)}
	if relayPolicyBackupRepo, ok := relayPolicyProjectionRepo.(repository.RelayPolicyProjectionBackupRepository); ok && servicePubkey != "" {
		backupRunOptions = append(backupRunOptions, service.WithRelayPolicyProjectionBackup(relayPolicyBackupRepo, servicePubkey))
		backupRestoreOptions = append(backupRestoreOptions, service.WithRelayPolicyProjectionRestore(relayPolicyBackupRepo, servicePubkey))
	}
	backupCoordinator := service.NewBackupRunCoordinator(backupRegistry, backupResolver, logger, backupRunOptions...)
	backupRestoreCoordinator := service.NewBackupRestoreCoordinator(backupRegistry, backupResolver, logger, backupRestoreOptions...)
	backupRetentionCoordinator := service.NewBackupRetentionCoordinator(backupRegistry, backupResolver, logger, service.WithBackupRetentionResponder(backupRetentionResponder))
	bgManager.RegisterWithOptions(backupCoordinator)
	bgManager.RegisterWithOptions(backupRestoreCoordinator)
	bgManager.RegisterWithOptions(backupRetentionCoordinator)

	backupScheduler := service.NewBackupSchedulerService(backupRegistry, logger,
		service.WithBackupSchedulerIdentity(servicePubkey),
	)
	backupSchedulerRunner := NewBackupSchedulerRunner(backupScheduler, 0, logger)
	bgManager.RegisterWithOptions(backupSchedulerRunner)
	healthProvider.RegisterCheck("backup_scheduler", func() HealthCheck {
		return HealthCheck{Name: "backup_scheduler", Status: HealthStatusPass, Message: "backup scheduler runner registered"}
	})
	logger.Info("backup control plane registered", zap.String("backend", string(domain.BackupBackendKopia)))

	// Generic AI/ML registry foundation. Bucket-B keeps this additive and keeps
	// long-running orchestration on the existing LLM path until dedicated buckets.
	var pgMLRegistryRepo repository.MLRegistryRepository
	if dbAvailable {
		pgMLRegistryRepo = repository.NewPgMLRegistryRepository(pool)
	}
	if err := repository.BootstrapLocalML(ctx, localOutbox, pgMLRegistryRepo); err != nil {
		return nil, fmt.Errorf("bootstrapping local ML registry: %w", err)
	}
	mlRegistryRepo := repository.NewLocalMLRegistryRepository(localOutbox, pgMLRegistryRepo)
	mlRegistry := service.NewMLRegistryService(mlRegistryRepo, publisher, logger, service.WithMLEnvironmentRepository(envRepo))
	workerReadModelSvc := service.NewWorkerReadModelService(workerRepo, registry, mlRegistry, workerPolicySvc, service.NewMLPlacementService(workerRepo, logger, service.WithMLPlacementPressureThresholds(pressureThresholds)), logger)
	workerCleanupOrchestrator := service.NewWorkerCleanupOrchestrator(workerRepo, workerReadModelSvc, loomCleanupClient{client: loomClient}, publisher, service.WorkerCleanupConfig{Mode: cfg.WorkerCleanup.Mode, Cooldown: cfg.WorkerCleanup.Cooldown, TargetFreeGB: cfg.WorkerCleanup.TargetFreeGB, PaymentToken: cfg.WorkerCleanup.PaymentToken, RequiredSoftware: cfg.WorkerCleanup.RequiredSoftware, PressureThresholds: pressureThresholds}, logger)
	setupWorkerPressureSubscriptions(publisher, pressureMonitor, workerStatePublisher, workerCleanupStatePublisher, workerCleanupOrchestrator, workerRepo, logger)

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

		gatewayHTTPConfig, err := llmGatewayHTTPConfig(cfg.LLM)
		if err != nil {
			return nil, fmt.Errorf("resolving LLM gateway authentication: %w", err)
		}
		gatewayManager := llmadapter.NewHTTPGatewayRouteManager(gatewayHTTPConfig, nil)
		provisioners := llmadapter.StaticProvisionerResolver{}
		llmSecretResolver := secretsAdapter.NewResolver(secretRepo, secretEncryptor)
		externalProvisioner := llmadapter.NewExternalAPIProvisioner(nil, llmadapter.WithExternalAPISecretResolver(llmSecretResolver))
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
			service.WithLLMCoordinatorRecoveryIntervals(cfg.LLM.RecoveryPollInterval, cfg.LLM.StaleRunTimeout),
			service.WithLLMPromotionLock(service.NewPGLLMPromotionLock(pool, logger)),
			service.WithLLMSecretResolver(llmSecretResolver),
		}
		if controlPlaneSigner != nil && controlPlanePool != nil {
			llmResponder = controlplane.NewLLMResponder(controlPlanePool, controlPlaneSigner, logger, nostrEventRepo)
			coordOpts = append(coordOpts, service.WithLLMProvisioningResponder(llmResponder))
		}
		llmCoordinator := service.NewLLMProvisioningCoordinator(llmRegistry, envRepo, llmRunRepo, placementSvc, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, logger, coordOpts...)
		llmCoordinator.SetupSubscriptions(publisher)
		llmReconciler := reconcile.NewLLMRouteReconciler(llmRegistry, envRepo, provisioners, gatewayManager, cfg.LLM.DefaultGatewayRef, logger, reconcile.WithLLMRouteSecretResolver(llmSecretResolver))
		llmReconciler.SetupSubscriptions(publisher)
		bgManager.RegisterWithOptions(llmCoordinator)
		bgManager.RegisterWithOptions(llmReconciler)
		logger.Info("LLM control plane enabled", zap.String("default_gateway_ref", cfg.LLM.DefaultGatewayRef))
	}

	var dnsProjector *reconcile.DNSProjector
	var dnsReconciler *reconcile.DNSReconciler
	var dnsZones []domain.DNSZone
	var dnsResolver *dnsAdapter.StaticResolver
	var dnsOperator controlplane.DNSControlPlaneOperator
	var dnsBackendClosers []io.Closer
	var dnsAgentHealthReader *dnsAdapter.AgentHealthReader
	var dnsZoneSyncPublisher *dnsAdapter.DeferredZoneSyncPublisher
	if cfg.DNS.Enabled {
		// Phase 3 D1: create agent health reader and deferred publisher for
		// capability-negotiated backend switching (C-34).
		dnsAgentHealthReader = dnsAdapter.NewAgentHealthReader(logger)
		dnsZoneSyncPublisher = &dnsAdapter.DeferredZoneSyncPublisher{}
		dnsZones, dnsResolver, dnsBackendClosers, err = buildDNSRuntime(ctx, cfg.DNS, controlPlaneRelays, controlPlaneSigner, servicePubkey, dnsAgentHealthReader, dnsZoneSyncPublisher, logger)
		if err != nil {
			return nil, err
		}
		if dnsZoneRepo != nil {
			for i := range dnsZones {
				if err := dnsZoneRepo.(*repository.LocalDNSZoneRepository).SeedConfigured(ctx, &dnsZones[i]); err != nil {
					return nil, fmt.Errorf("persisting configured DNS zone %q: %w", dnsZones[i].Name, err)
				}
			}
			persistedZones, err := dnsZoneRepo.List(ctx)
			if err != nil {
				return nil, fmt.Errorf("loading persisted DNS zones: %w", err)
			}
			dnsZones = persistedZones
		}
		dnsProjector = reconcile.NewDNSProjector(serviceRepo, envRepo, stateRepo, obsRepo, llmRegistry, mlRegistry, workerRepo, cfg.DNS, logger)
		dnsProjector.SetManualEndpointSource(dnsEndpointRepo)
		dnsProjector.SetZoneSource(dnsZoneRepo)
		dnsProjector.SetContinuityStatusReader(continuityDNSStatusReader{reader: continuityStatusStore})
		if policySource, ok := dnsPolicyRepo.(reconcile.DNSPolicySource); ok {
			dnsProjector.SetPolicySource(policySource)
		}
		dnsReconciler = reconcile.NewDNSReconciler(dnsProjector, dnsZones, dnsResolverBridge{resolver: dnsResolver}, cfg.DNS.ReconcileInterval, logger)
		dnsReconciler.SetPublisher(publisher)
		dnsReconciler.SetPersistenceSources(dnsZoneRepo, dnsRecordOverrideRepo)
		dnsReconciler.SetupSubscriptions(publisher)
		var dnsPersistence controlplane.DNSPersistenceOperator
		if dnsZoneRepo != nil && dnsRecordOverrideRepo != nil {
			dnsPersistence = dnsRepositoryPersistenceAdapter{zones: dnsZoneRepo, overrides: dnsRecordOverrideRepo}
		}
		dnsOperator = newDNSControlPlaneOperator(dnsReconciler, dnsZones, dnsResolver.Refs(), dnsPersistence, dnsPolicyRepo)
		configuredBackends := (configDNSBackendProjectionSource{backends: cfg.DNS.Backends, zones: dnsZones, resolver: dnsResolver}).ListDNSBackendStates(ctx)
		for i := range configuredBackends {
			if err := dnsBackendRepo.(*repository.LocalDNSBackendRepository).SeedConfigured(ctx, &configuredBackends[i]); err != nil {
				return nil, fmt.Errorf("persisting configured DNS backend %q: %w", configuredBackends[i].Ref, err)
			}
		}
		bgManager.RegisterWithOptions(dnsReconciler)

		// Phase 3 D1: subscribe to NIP-38 agent health events so the daemon
		// reads agent health and capabilities from events instead of RPC.
		if dnsAgentHealthReader != nil {
			var agentPubkeys []string
			for _, backendCfg := range cfg.DNS.Backends {
				if backendCfg.Type == string(domain.DNSBackendTypeDnsmasqAgent) && backendCfg.AgentPubkey != "" {
					agentPubkeys = append(agentPubkeys, backendCfg.AgentPubkey)
				}
			}
			if len(agentPubkeys) > 0 {
				bgManager.RegisterWithOptions(&agentHealthSubscriber{
					pool:    controlPlanePool,
					pubkeys: agentPubkeys,
					reader:  dnsAgentHealthReader,
					logger:  logger,
				})
				logger.Info("DNS agent health subscriber registered", zap.Int("agents", len(agentPubkeys)))
			}
		}
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
	// the 310xx audit/activity feed for relay consumers. It publishes through
	// the control-plane outbox publisher, so every projection gets an outbox
	// row and per-relay retry to the control-plane relays.
	projectorOpts := []nostrAdapter.ProjectorOption{
		// Phase 3 B1: WithBackupProjectionSource removed. Canonical records
		// are now published by BackupCanonicalPublisher wired to the registry.
		nostrAdapter.WithMLProjectionSource(mlRegistry),
		nostrAdapter.WithWorkerProjectionSource(workerRepo),
		// Phase 3 W1: WithWorkerReadModelProjectionSource removed — worker read
		// models are published directly from the mutation site (bahia-irsry.11.14).
		nostrAdapter.WithSystemDiscoveryConfig(cfg, true),
	}
	if dnsProjector != nil {
		projectorOpts = append(projectorOpts,
			nostrAdapter.WithDNSProjectionSource(dnsProjector),
			nostrAdapter.WithDNSZoneProjectionSource(staticDNSZoneProjectionSource{zones: dnsZones}),
			nostrAdapter.WithDNSBackendProjectionSource(localDNSBackendProjectionSource{repo: dnsBackendRepo, logger: logger}),
		)
	}
	if dnsPolicyRepo != nil {
		projectorOpts = append(projectorOpts, nostrAdapter.WithDNSPolicyProjectionSource(dnsPolicyRepositoryProjectionSource{repo: dnsPolicyRepo}))
	}
	// Phase 3 X1: WithSBOMProjectionSource removed. SBOM events are published
	// from the SBOM orchestrator's mutation site (bahia-irsry.11.17).
	// Phase 3 X1: warm-start covers ALL cp-state domains. Every domain's
	// canonical records are now published from mutation sites; warm-start
	// re-publishes only stale or missing records on restart.
	warmStartDomains := nostrAdapter.CPStateDomains()
	if len(enabledDomains) == 0 {
		// No intent subscriber → readiness has no filters. Register and
		// immediately satisfy a sentinel so warm-start proceeds.
		intentReadiness.RegisterFilter("authoritative-warmstart")
		intentReadiness.MarkFilterReady("authoritative-warmstart")
	}
	projectorOpts = append(projectorOpts,
		nostrAdapter.WithReadinessTracker(intentReadiness),
		nostrAdapter.WithIntentDomains(warmStartDomains),
	)
	// The projector's memory of what it published is its own latest events
	// in the local event store, never PostgreSQL (B-3).
	projectionHistory := nostrAdapter.NewLocalEventRepository(localEventStore, nil).Authored(servicePubkey)
	nostrProjector := nostrAdapter.NewProjector(cfg.Nostr, registry, controlPlanePub, projectionHistory, logger, projectorOpts...)
	controlPlanePub.OnDeliveryAbandoned(nostrProjector.ForgetAbandonedProjection)

	// F74b: mutation-bound publishers for package intent/approval and tool
	// provisioning. OCK is installed below before any ingress starts.
	f74bCanonical := nostrAdapter.NewF74bCanonicalPublisher(nostrProjector, nil)
	if packageProjection != nil {
		packageAuth, ok := packageProjection.(repository.PackageAuthorizationStore)
		if !ok {
			return nil, fmt.Errorf("package projection lacks authorization store")
		}
		packageProjection = nostrAdapter.NewCanonicalPackageRepository(packageProjection, packageAuth, f74bCanonical)
	}
	if toolProvisionRepo != nil {
		toolProvisionRepo = nostrAdapter.NewCanonicalToolRepository(toolProvisionRepo, f74bCanonical)
	}

	// Relay-first write path: when mode is not "full" OR when explicitly enabled,
	// wrap registry mutations so relay publish must succeed before local DB writes.
	// In full mode, this defaults off for backward compatibility with existing
	// DB-first semantics. Set mode to degraded/emergency or configure
	// relay_canonical_writes: true to activate.
	// The records go to the control-plane relays the projector publishes the
	// same coordinates to, built and signed through the projector's own
	// builders and coordinate state, so both writers emit one record shape
	// and a projection of an unchanged state is not re-signed. Once the
	// quorum accepted, the control-plane outbox retries the remaining relays
	// (bahia-irsry.41).
	var relayFirstRegistry *service.RelayFirstRegistry
	if cfg.Nostr.PublishEnabled || cfg.Mode == "" || cfg.Mode == "full" {
		statePublisher := nostrAdapter.NewRelayFirstStatePublisher(nostrProjector, controlPlanePub)
		relayFirstRegistry = service.NewRelayFirstRegistry(registry, statePublisher, logger)
		// Phase 3 S2: wire the cp-state publisher for build/artifact/intent/run
		// families so RegistryService publishes canonical state directly from
		// its mutation methods (bahia-irsry.11.7).
		registry.SetCPStatePublisher(statePublisher)
		logger.Info("relay-first write path enabled for core registry mutations",
			zap.String("mode", "full"))
	}
	// Phase 3 F3: register environment intent handler when "environment" is
	// enabled by the computed domain set. Uses the relay-first registry (which publishes the
	// canonical 30900 via PublishBeforeCommit) or falls back to the plain
	// registry when relay-first is not configured.
	if enabledDomains["environment"] {
		var envRegistry service.EnvironmentIntentRegistry
		if relayFirstRegistry != nil {
			envRegistry = relayFirstRegistry
		} else {
			envRegistry = registry
		}
		envHandler := controlplane.NewEnvironmentIntentHandler(
			envRegistry,
			nil, // statePublisher: the relay-first registry handles publishing
			logger,
		)
		intentProcessor.RegisterHandler("environment", envHandler)
		logger.Info("environment intent handler registered")
	}

	// Phase 3 S1: wire the reconciler's direct state publisher and tombstone
	// handler so runtime state is published to relays without the projector.
	if rec != nil && relayFirstRegistry != nil {
		statePublisher := nostrAdapter.NewRelayFirstStatePublisher(nostrProjector, controlPlanePub)
		rec.SetRuntimeStatePublisher(statePublisher)
		tombstoneHandler := reconcile.NewStateTombstoneHandler(statePublisher, logger)
		tombstoneHandler.SetupSubscriptions(publisher)
		logger.Info("runtime state direct publisher and tombstone handler wired (Phase 3 S1)")
	}

	nostrProjector.SetupSubscriptions(publisher)

	// --- Phase 3 X1: Adoption canonical publisher ---
	// Publishes service-registry and environment-registry cp-state records
	// directly from the adoption import site instead of reactively through
	// the projector's EventAdoptionImported handler (bahia-irsry.11.17).
	if adoptionSvc != nil {
		adoptionCanonical := nostrAdapter.NewAdoptionCanonicalPublisher(nostrProjector, logger)
		adoptionSvc.SetAdoptionCanonicalPublisher(adoptionCanonical)
	}

	// --- Phase 3 B1: Backup canonical publisher and intent handler ---
	// BackupCanonicalPublisher follows the MLCanonicalPublisher pattern: holds
	// a *Projector reference and publishes through the shared signing/outbox
	// pipeline. The cpStateFamilies table is the single envelope source.
	backupCanonical := nostrAdapter.NewBackupCanonicalPublisher(nostrProjector, logger)
	backupCanonical.SetRunVerifier(backupRegistry)
	backupCanonical.SetRuntimeObservationSource(backupRegistry)
	backupRegistry.SetCanonicalPublisher(backupCanonical)
	// Notifier hook: wire Trigger() on coordinators after every registry
	// mutation so coordinators wake immediately on new/requeued work.
	backupRegistry.SetNotifyHook(func() {
		backupCoordinator.Trigger()
		backupRestoreCoordinator.Trigger()
		backupRetentionCoordinator.Trigger()
		backupSchedulerRunner.Trigger()
	})
	// Register the intent handler when the backup domain is enabled.
	if enabledDomains["backup"] && backupRegistry != nil {
		intentProcessor.RegisterHandler("backup", controlplane.NewBackupIntentHandler(
			controlplane.BackupIntentHandlerConfig{
				Registry:    backupRegistry,
				Definitions: backupRegistry,
				Publisher:   backupCanonical,
				Executors: controlplane.BackupIntentExecutors{
					RunExecutor:       backupCoordinator,
					RestoreExecutor:   backupRestoreCoordinator,
					RetentionExecutor: backupRetentionCoordinator,
				},
				Status: intentStatus,
				Logger: logger,
			},
		))
		logger.Info("backup intent handler registered")
	}
	// --- end B1 wiring ---

	var dnsCanonicalPub *nostrAdapter.DNSCanonicalPublisher
	// Phase 3 D1: wire canonical DNS publisher. The reconciler calls this after
	// each material reconcile so DNS records publish once per mutation instead
	// of O(fleet) per projector tick (B-17).
	if dnsReconciler != nil {
		dnsCanonicalPub = nostrAdapter.NewDNSCanonicalPublisher(nostrProjector, logger)
		if err := dnsCanonicalPub.HydrateFromStore(ctx); err != nil {
			logger.Warn("DNS canonical publisher hydration failed", zap.Error(err))
		}
		dnsReconciler.SetCanonicalPublisher(dnsCanonicalPub)
		// Wire the deferred zone sync publisher to the real canonical publisher
		// so the capability-aware backend can publish zone sync events.
		if dnsZoneSyncPublisher != nil {
			dnsZoneSyncPublisher.SetDelegate(dnsCanonicalPub)
		}
		logger.Info("DNS canonical publisher wired to reconciler (Phase 3 D1)")
	}

	// --- D70 DNS intent registration (kept separate from D69 app wiring) ---
	if enabledDomains["dns"] && nostrProjector.Enabled() && dnsOperator != nil && dnsCanonicalPub != nil {
		mutations := &service.DNSMutationService{Zones: dnsZoneRepo, Policies: dnsPolicyRepo, Endpoints: dnsEndpointRepo, Backends: dnsBackendRepo, Canonical: dnsCanonicalPub, Reconciler: dnsOperator.(service.DNSMutationReconciler)}
		intentProcessor.RegisterHandler("dns", controlplane.NewDNSIntentHandler(dnsOperator, dnsCanonicalPub, mutations))
	}
	// --- end D70 DNS intent registration ---

	// Phase 3 F2: register service domain intent handler.
	// Uses the relay-first registry when available (canonical 30900 published
	// before DB write), falling back to the plain registry.
	{
		var serviceMutationBackend controlplane.RegistryMutationBackend = registry
		if relayFirstRegistry != nil {
			serviceMutationBackend = relayFirstRegistry
		}
		serviceIntentHandler := controlplane.NewServiceIntentHandler(
			controlplane.ServiceIntentHandlerConfig{
				Registry: serviceMutationBackend,
				Reader:   serviceRepo,
				Logger:   logger,
			},
		)
		intentProcessor.RegisterHandler("service", serviceIntentHandler)
	}

	// --- D76 artifact intent registration (separate from D77 build handlers) ---
	// RegistryService owns build/artifact cp-state publication. Do not pass its
	// relay-first wrapper here: that would invoke the same publisher twice.
	if enabledDomains["artifact"] {
		intentProcessor.RegisterHandler("artifact", controlplane.NewArtifactIntentHandler(registry, serviceRepo))
	}
	if enabledDomains["adoption"] && adoptionSvc != nil {
		intentProcessor.RegisterHandler("adoption", controlplane.NewAdoptionIntentHandler(adoptionSvc, cfg.Adoption.AllowedPubkeys))
	}
	// --- end D76 artifact intent registration ---

	// Phase 3 S3: PolicyStatePublisher for canonical 30900 via PublishBeforeCommit.
	// Created unconditionally so both the legacy (non-intent) ContextVM path and
	// the intent handler path use the same sign-and-publish closure. Fingerprint
	// dedupe prevents double-signing if both paths ever fire for the same entity
	// in a single process lifetime (belt-and-suspenders; the dual-dispatch guard
	// makes this unreachable in normal operation).
	var policyPublisher controlplane.PolicyStatePublisher
	if nostrPub != nil && controlPlaneSigner != nil {
		var policyPubMu sync.Mutex
		policyFingerprints := make(map[string]struct{})
		policyLastPublishedAt := make(map[uuid.UUID]nostr.Timestamp)
		policyPublisher = func(ctx context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
			fp := fmt.Sprintf("%s:%t:%d", policy.ID, deleted, policy.UpdatedAt.UnixNano())
			policyPubMu.Lock()
			if _, dup := policyFingerprints[fp]; dup {
				policyPubMu.Unlock()
				return nil // already published for this mutation
			}
			policyFingerprints[fp] = struct{}{}
			// Monotonic timestamp: relays replace an addressable event only
			// with a newer event on the same (kind, pubkey, d) coordinate.
			createdAt := nostr.Now()
			if last := policyLastPublishedAt[policy.ID]; createdAt <= last {
				createdAt = last + 1
			}
			policyLastPublishedAt[policy.ID] = createdAt
			policyPubMu.Unlock()
			recordTags, recordContent := controlplane.PolicyRegistryRecord(policy, deleted)
			deletedStr := "false"
			if deleted {
				deletedStr = "true"
			}
			tags := nostr.Tags{
				{"d", policy.ID.String()},
				{"domain", "policy"},
				{"schema", "bahia.cp-state.v1"},
				{"legacy_kind", fmt.Sprintf("%d", nostrAdapter.KindPolicyRegistry)},
				{"deleted", deletedStr},
				{"t", kinds.CPStateTopicPolicyRegistry},
			}
			tags = append(tags, recordTags...)
			ev := nostr.Event{
				Kind:      nostr.Kind(nostrAdapter.KindCASControlState),
				CreatedAt: createdAt,
				Tags:      tags,
				Content:   recordContent,
			}
			if err := controlplane.SignGoNostrEvent(ctx, controlPlaneSigner, &ev); err != nil {
				return fmt.Errorf("sign policy state event: %w", err)
			}
			return nostrPub.PublishBeforeCommit(ctx, ev, "policy", &policy.ID)
		}
	}
	// Register the intent handler when the policy domain is enabled.
	if enabledDomains["policy"] && policySvc != nil {
		intentProcessor.RegisterHandler("policy", controlplane.NewPolicyIntentHandler(
			controlplane.PolicyIntentHandlerConfig{
				Policies:  policySvc,
				Evaluator: policySvc,
				Publish:   policyPublisher,
				Status:    intentStatus,
				Logger:    logger,
			},
		))
		logger.Info("policy intent handler registered")
	}

	// Phase 3 L1: LLMRouteStatePublisher for canonical 30900 via PublishBeforeCommit.
	// Created unconditionally so both the legacy (non-intent) ContextVM path and
	// the intent handler path use the same sign-and-publish closure.
	var llmRoutePublisher controlplane.LLMRouteStatePublisher
	if nostrPub != nil && controlPlaneSigner != nil && llmRegistry != nil {
		var llmPubMu sync.Mutex
		llmFingerprints := make(map[string]struct{})
		llmLastPublishedAt := make(map[uuid.UUID]nostr.Timestamp)
		llmRoutePublisher = func(ctx context.Context, route *domain.LLMRoute, deleted bool) error {
			fp := fmt.Sprintf("%s:%t:%d", route.ID, deleted, route.UpdatedAt.UnixNano())
			llmPubMu.Lock()
			if _, dup := llmFingerprints[fp]; dup {
				llmPubMu.Unlock()
				return nil
			}
			llmFingerprints[fp] = struct{}{}
			createdAt := nostr.Now()
			if last := llmLastPublishedAt[route.ID]; createdAt <= last {
				createdAt = last + 1
			}
			llmLastPublishedAt[route.ID] = createdAt
			llmPubMu.Unlock()
			recordTags, recordContent := controlplane.LLMRouteRegistryRecord(route, deleted)
			deletedStr := "false"
			if deleted {
				deletedStr = "true"
			}
			tags := nostr.Tags{
				{"d", route.ID.String()},
				{"domain", "llm-route"},
				{"schema", "bahia.cp-state.v1"},
				{"legacy_kind", fmt.Sprintf("%d", nostrAdapter.KindLLMRouteRegistry)},
				{"deleted", deletedStr},
				{"t", kinds.CPStateTopicLLMRoute},
			}
			tags = append(tags, recordTags...)
			ev := nostr.Event{
				Kind:      nostr.Kind(nostrAdapter.KindCASControlState),
				CreatedAt: createdAt,
				Tags:      tags,
				Content:   recordContent,
			}
			if err := controlplane.SignGoNostrEvent(ctx, controlPlaneSigner, &ev); err != nil {
				return fmt.Errorf("sign LLM route state event: %w", err)
			}
			return nostrPub.PublishBeforeCommit(ctx, ev, "llm_route", &route.ID)
		}
	}
	// Register the intent handler when the llm domain is enabled.
	if enabledDomains["llm"] && llmRegistry != nil {
		intentProcessor.RegisterHandler("llm", controlplane.NewLLMRouteIntentHandler(
			controlplane.LLMRouteIntentHandlerConfig{
				Routes:  llmRegistry,
				Publish: llmRoutePublisher,
				Status:  intentStatus,
				Logger:  logger,
			},
		))
		logger.Info("LLM route intent handler registered")
	}
	// Phase 3 P1: register package intent handler, publishing through the
	// shared cp-state path (controlStateEnvelope + publishAuthoritative).
	if enabledDomains["package"] && packageRegistrySvc != nil {
		packageAuthStore, _ := packageProjection.(repository.PackageAuthorizationStore)
		var packageWriter controlplane.PackageCPStateWriter
		if nostrProjector != nil && controlPlanePub != nil {
			packageWriter = nostrAdapter.NewRelayFirstStatePublisher(nostrProjector, controlPlanePub)
		}
		intentProcessor.RegisterHandler("package", controlplane.NewPackageIntentHandler(
			controlplane.PackageIntentHandlerConfig{
				PackageService: packageRegistrySvc,
				Projection:     packageProjection,
				Store:          packageAuthStore,
				Writer:         packageWriter,
				StatePublisher: f74bCanonical,
				Status:         intentStatus,
				Gate:           controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys),
				Logger:         logger,
			},
		))
		logger.Info("package intent handler registered")
	}

	// Phase 3 C1: unified confidential cp-state crypto (§1.7).
	// Per-org content key (OCK) encrypted with XChaCha20-Poly1305 AEAD.
	// OCK distributed to org members + service via NIP-44 through signer interface.
	// Replaces both the O1 sha256-derived key path and the N1 NIP-44 self-encryption.
	//
	// Legacy O1 encryptor retained for dual-read during migration.
	var legacyO1Encryptor *controlplane.OrgStateEncryptorImpl
	if cfg.Nostr.PrivateKey != "" {
		orgKeySum := sha256.Sum256([]byte("bahia org state key v1\x00" + strings.TrimSpace(cfg.Nostr.PrivateKey)))
		legacyO1Encryptor = controlplane.NewOrgStateEncryptor(controlplane.StaticOrgStateKeyProvider{
			Key: controlplane.OrgStateKey{
				Ref:     "org-state/service-nostr-key",
				Version: "v1",
				Key:     orgKeySum[:],
			},
		})
	}

	// Phase 3 C1: create OCKManager and ConfidentialEncryptor.
	var confidentialEncryptor *controlplane.ConfidentialEncryptor
	if controlPlaneSigner != nil && servicePubkey != "" {
		ockHistory := nostrAdapter.NewProjectorOCKEnvelopeHistory(projectionHistory)
		ockMemberSource := controlplane.NewTrustSetMemberSource(trustSet, orgMemberRepo)
		ockManager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
			Signer:        controlPlaneSigner,
			ServicePubkey: servicePubkey,
			Publisher:     nostrProjector, // implements OCKEnvelopePublisher via structural typing
			History:       ockHistory,     // implements OCKEnvelopeHistory via structural typing
			Members:       ockMemberSource,
			Logger:        logger,
		})
		confidentialEncryptor = controlplane.NewConfidentialEncryptor(ockManager, logger)
		f74bCanonical.SetEncryptor(confidentialEncryptor)
	} else if enabledDomains["org"] {
		logger.Error("org domain requires control-plane signer for confidential state; " +
			"disabling org domain to prevent plaintext state publication")
		delete(enabledDomains, "org")
	}

	// Phase 3 C1: create encrypted canonical publisher for org state.
	var orgCanonicalPub *nostrAdapter.OrgCanonicalPublisher
	if nostrProjector != nil && confidentialEncryptor != nil {
		orgCanonicalPub = nostrAdapter.NewOrgCanonicalPublisher(nostrProjector, confidentialEncryptor, logger)
	}

	// Phase 3 O1: register org intent handler when "org" is enabled.
	// The handler processes org/member/invite intents and publishes canonical
	// cp-state through the OrgCanonicalPublisher with encrypted content (§1.7).
	if enabledDomains["org"] && orgRepo != nil && orgMemberRepo != nil && orgInviteRepo != nil {
		orgHandler := controlplane.NewOrgIntentHandler(controlplane.OrgIntentHandlerConfig{
			Orgs:      orgRepo,
			Members:   orgMemberRepo,
			Invites:   orgInviteRepo,
			Publisher: orgCanonicalPub,
			Status:    intentStatus,
			Logger:    logger,
			OnMemberChange: func(orgID uuid.UUID) {
				// Rebuild relay members for this org from Postgres (interim
				// until the local store hydration replaces this). The published
				// encrypted events are the source of truth; the callback fires
				// after both Postgres and relay publishes complete.
				members, err := orgMemberRepo.ListByOrg(ctx, orgID)
				if err != nil {
					logger.Warn("failed to list org members for TrustSet update",
						zap.String("org_id", orgID.String()), zap.Error(err))
					return
				}
				roleMap := make(map[string]domain.Role, len(members))
				for _, m := range members {
					roleMap[m.Pubkey] = m.Role
				}
				trustSet.SetRelayMembers(orgID.String(), roleMap)
				if intentAuthorsSyncer != nil {
					intentAuthorsSyncer.Notify()
				}
				logger.Debug("TrustSet relay members updated for org",
					zap.String("org_id", orgID.String()),
					zap.Int("member_count", len(members)))
			},
		})
		intentProcessor.RegisterHandler("org", orgHandler)
		logger.Info("org intent handler registered")
	}

	// Phase 3 O1: gift-wrapped intent ingress for sensitive domains (§1.7).
	// Shared by O1 (org) and N1 (secret, notification). Plaintext 30900 intents
	// for these domains are rejected with a bounded status.
	var giftWrapIngress *controlplane.IntentGiftWrapIngress
	if controlPlaneSigner != nil {
		giftWrapIngress = controlplane.NewIntentGiftWrapIngress(controlplane.IntentGiftWrapIngressConfig{
			Signer:           controlPlaneSigner,
			Processor:        intentProcessor,
			SensitiveDomains: []string{"org", "secret", "notification"},
			Logger:           logger,
		})
		intentProcessor.SetGiftWrapIngress(giftWrapIngress)
		logger.Info("gift-wrap intent ingress registered for sensitive domains")
	}
	// Phase 3 C1: relay member event handler for TrustSet hydration from
	// encrypted membership events (§2.5 item 3). Wired for warm-start and
	// live member publishes. Even when Postgres is configured, the relay
	// source has highest precedence in TrustSet resolution.
	// Uses the new confidential encryptor with legacy O1 fallback for
	// dual-read during migration.
	var relayMemberEventHandler *controlplane.RelayMemberEventHandler
	if confidentialEncryptor != nil || legacyO1Encryptor != nil {
		relayMemberEventHandler = controlplane.NewRelayMemberEventHandler(
			confidentialEncryptor, legacyO1Encryptor, trustSet, orgMemberRepo, logger,
		)

		// (ii) Live: after OrgCanonicalPublisher publishes a member record
		// (both legacy ContextVM and intent paths), feed it through the handler
		// so TrustSet relay members stay in sync in real time.
		if orgCanonicalPub != nil {
			orgCanonicalPub.SetOnMemberPublished(func(ctx context.Context, encryptedContent string, legacyKind int, dTag, topic string) {
				if err := relayMemberEventHandler.HandleEncryptedMemberEvent(ctx, encryptedContent, legacyKind, dTag, topic); err != nil {
					logger.Debug("relay member event handler: post-publish hydration failed",
						zap.Error(err))
				}
				if intentAuthorsSyncer != nil {
					intentAuthorsSyncer.Notify()
				}
			})
		}

		// (i) Startup: hydrate TrustSet from the daemon's own published
		// encrypted membership events in history. This runs before the intent
		// subscriber's author filter is computed, ensuring relay-sourced members
		// are included in the authors set from the start.
		if nostrProjector != nil {
			relayMemberEventHandler.HydrateTrustSetFromHistory(ctx, projectionHistory)
		}

		logger.Info("relay member event handler created and wired for TrustSet hydration")
	}

	// Phase 3 L1: wire LLM route state cp-state publisher into the registry service
	// so state mutations publish 30900 records directly instead of through the projector.
	if nostrPub != nil && controlPlaneSigner != nil && llmRegistry != nil {
		var llmStatePubMu sync.Mutex
		llmStateFingerprints := make(map[string]struct{})
		llmRegistry.SetLLMCPStatePublisher(func(ctx context.Context, state *domain.LLMRouteState) {
			fp := fmt.Sprintf("%s:%s:%d", state.RouteID, state.EnvironmentID, state.UpdatedAt.UnixNano())
			llmStatePubMu.Lock()
			if _, dup := llmStateFingerprints[fp]; dup {
				llmStatePubMu.Unlock()
				return
			}
			llmStateFingerprints[fp] = struct{}{}
			llmStatePubMu.Unlock()
			recordTags, recordContent := controlplane.LLMRouteStateRecord(state)
			dTag := controlplane.LLMRouteStateDTag(state.RouteID, state.EnvironmentID)
			tags := nostr.Tags{
				{"d", dTag},
				{"domain", "llm-state"},
				{"schema", "bahia.cp-state.v1"},
				{"legacy_kind", fmt.Sprintf("%d", nostrAdapter.KindLLMRouteState)},
				{"deleted", "false"},
				{"t", kinds.CPStateTopicLLMState},
			}
			tags = append(tags, recordTags...)
			ev := nostr.Event{
				Kind:      nostr.Kind(nostrAdapter.KindCASControlState),
				CreatedAt: nostr.Now(),
				Tags:      tags,
				Content:   recordContent,
			}
			if err := controlplane.SignGoNostrEvent(ctx, controlPlaneSigner, &ev); err != nil {
				logger.Warn("sign LLM route state event failed", zap.Error(err))
				return
			}
			if err := nostrPub.PublishBeforeCommit(ctx, ev, "llm_route_state", nil); err != nil {
				logger.Warn("publish LLM route state cp-state failed", zap.Error(err))
			}
		})
	}
	// F74a: separate wiring block for release, signature, artifact-SBOM and
	// latest runtime-observation families. Each writer keeps its existing DB path.
	if nostrProjector != nil && nostrProjector.Enabled() {
		f74aCanonical := nostrAdapter.NewF74aCanonicalPublisher(nostrProjector, confidentialEncryptor, localOutbox)
		if llmRegistry != nil {
			llmRegistry.SetReleaseCPStatePublisher(f74aCanonical)
		}
		registry.SetObservationCPStatePublisher(f74aCanonical)
		if sigRepo != nil {
			sigRepo = service.NewCanonicalSignatureRepository(sigRepo, f74aCanonical, logger)
		}
		if sbomRepo != nil {
			sbomRepo = service.NewCanonicalSBOMRepository(sbomRepo, f74aCanonical, logger)
		}
		if sbomManifestRepo != nil && sbomRepo != nil {
			sbomManifestRepo = service.NewCanonicalSBOMManifestRepository(sbomManifestRepo, sbomRepo, f74aCanonical, logger)
		}
		if dbAvailable && pool != nil {
			var llmBackfill service.F74aReleaseLister
			if llmRegistry != nil {
				llmBackfill = llmRegistry
			}
			if err := service.BootstrapF74aCanonical(ctx, service.F74aBackfillConfig{
				Marker: localOutbox, Publisher: f74aCanonical, LLM: llmBackfill,
				Services: serviceRepo, Artifacts: artifactRepo, Signatures: sigRepo,
				SBOMs: f74aSBOMBackfill, Observations: obsRepo, States: stateRepo,
			}); err != nil {
				return nil, fmt.Errorf("backfill F74a canonical state: %w", err)
			}
		}
	}

	// Phase 3 M1: wire ML cp-state publisher into registry service so state
	// mutations publish canonical records directly instead of through the projector.
	if nostrProjector.Enabled() && mlRegistry != nil {
		mlCanonicalPub := nostrAdapter.NewMLCanonicalPublisher(nostrProjector, logger)
		mlRegistry.SetMLCPStatePublisher(mlCanonicalPub)
		logger.Info("ML canonical cp-state publisher wired into registry service")
	}

	// --- D70 ML intent registration (kept separate from D69 app wiring) ---
	if enabledDomains["ml"] && nostrProjector.Enabled() && mlRegistry != nil {
		intentProcessor.RegisterHandler("ml", controlplane.NewMLIntentHandler(mlRegistry, registry))
	}
	// --- end D70 ML intent registration ---

	// Phase 3 §1.7: Legacy OCK migration — re-publish legacy-format
	// confidential records under the per-org content key scheme at startup.
	// The migrator runs as a post-warm-start hook on the projector so that
	// history is up to date from all relays before scanning.
	if nostrProjector != nil && confidentialEncryptor != nil {
		ockMigrator := nostrAdapter.NewLegacyOCKMigrator(
			nostrProjector, confidentialEncryptor, legacyO1Encryptor, logger,
		)
		nostrProjector.AddPostWarmStartHook(ockMigrator.Run)
		logger.Info("legacy OCK migrator registered as post-warm-start hook")
	}
	if nostrProjector.Enabled() {
		bgManager.RegisterWithOptions(nostrProjector)
		logger.Info("nostr read-model projector registered")
	}

	// Virtualization remains unavailable until persistence, projection and the
	// explicitly configured provider/plane trust boundaries are all ready.
	virtualizationStore, _ := nostrEventRepo.(readmodel.VirtualizationProjectionStore)
	var virtualizationPublisher readmodel.VirtualizationSignedPublisher
	if cfg.Nostr.PublishEnabled && controlPlaneSigner != nil {
		virtualizationPublisher = nostrPub
	}
	virtualizationDeps := VirtualizationDependencies{
		Repository: virtualizationRepo, RBAC: tenantRBAC, Bus: publisher,
		Store: virtualizationStore, Publisher: virtualizationPublisher, Organizations: orgRepo,
		CanonicalAuthor: servicePubkey,
	}
	var vmSecretResolver service.VMAuditedSecretResolver
	if secretRepo != nil && secretEncryptor != nil {
		vmSecretResolver = secretsAdapter.NewResolver(secretRepo, secretEncryptor)
	}
	if err := configureVirtualization(ctx, cfg.Virtualization, &virtualizationDeps, virtualizationConfigurationDependencies{
		events: nostrEventRepo, secrets: secretRepo, resolver: vmSecretResolver,
		services: serviceRepo, environments: envRepo, units: deploymentUnitRepo, workers: workerRepo,
		pool: controlPlanePool, signer: controlPlaneSigner, logger: logger,
	}); err != nil {
		return nil, fmt.Errorf("configure virtualization services: %w", telemetry.SanitizedVirtualizationError(err))
	}
	virtualization, err := NewVirtualization(virtualizationDeps)
	if err != nil {
		return nil, fmt.Errorf("configure virtualization: %w", err)
	}
	loom.WithVerifiedPlaneCapabilities(virtualization.VerifiedPlanes())(loomClient)
	if virtualization.Projector != nil {
		bgManager.RegisterWithOptions(virtualization)
	}

	// Register the reconciler as a background runner (if enabled).
	if rec != nil {
		bgManager.RegisterWithOptions(&reconcilerRunner{rec: rec})
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
	// F75 operational view publications: Soul runtime policy and one bounded
	// Blossom startup observation; daemon-owned uploads publish at their site.
	if nostrProjector != nil && nostrProjector.Enabled() {
		viewPublisher := nostrAdapter.NewOperationalViewPublisher(nostrProjector, confidentialEncryptor)
		owners := append([]string{blossomOwnerKey(cfg.Blossom.PrivateKey)}, cfg.Nostr.AuthorizedPubkeys...)
		for _, owner := range cfg.Nostr.BootstrapOwners {
			owners = append(owners, owner)
		}
		if blossomClient != nil && confidentialEncryptor != nil {
			owner := blossomOwnerKey(cfg.Blossom.PrivateKey)
			if owner != "" {
				blossomClient.SetUploadObserver(func(ctx context.Context, descriptor blossom.BlobDescriptor) error {
					return viewPublisher.PublishBlossomBlob(ctx, owner, descriptor)
				})
			}
		}
		bgManager.RegisterWithOptions(&operationalViewsRunner{
			publisher: viewPublisher, blossom: blossomClient,
			runtimes: append([]string{}, cfg.SoulFactory.AgentRuntimes...),
			owners:   owners, logger: logger,
		}, RunnerRequired(false))
	}
	var runLogService *runtime.LogService
	var sbomOrchestrator *service.SBOMOrchestrator
	var sbomStorageResolver *sbomAdapter.StorageResolver
	if blossomClient != nil {
		runLogService = runtime.NewLogService(blossomClient, nil, logger)
		generatorRegistry, err := newSBOMGeneratorRegistry(cfg.SBOM)
		if err != nil {
			return nil, fmt.Errorf("create SBOM generator registry: %w", err)
		}
		sbomStorageResolver = sbomAdapter.NewStorageResolver(blossomClient, nil, nil, slog.Default())
		attestationSigner, err := sbomAdapter.NewNostrDSSESigner(cfg.Nostr.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("configure SBOM attestation signer: %w", err)
		}
		sbomOrchestrator = service.NewSBOMOrchestrator(service.SBOMOrchestratorConfig{
			Generators:        generatorRegistry,
			Storage:           sbomStorageResolver,
			Repo:              sbomManifestRepo,
			Publisher:         sbomPublishAdapter{publisher: controlPlanePub},
			Subscriber:        sbomAvailabilityRelaySubscriber{pool: controlPlanePool},
			AttestationSigner: attestationSigner,
			Resolver: service.SBOMSubjectResolver{
				Artifacts:   artifactRepo,
				Deployments: intentRepo,
				Packages:    packageProjection,
				Services:    serviceRepo,
			},
			Pubkey: servicePubkey,
			Logger: logger,
		})
		// Manifests recorded as pending on a queued reference become
		// published when the control-plane outbox delivers that reference,
		// and failed if it abandons it.
		controlPlanePub.OnDeliveryAbandoned(sbomOrchestrator.HandlePublishAbandoned)
		controlPlanePub.OnDelivered(sbomOrchestrator.HandlePublishDelivered)
	}

	// OCI Registry wiring.
	var ociHandler http.Handler
	var ociRepo repository.OCIRegistryRepository
	var ociSvc *service.OCIRegistryService
	if cfg.OCI.Enabled {
		pgOCIRepo := repository.NewPgOCIRepository(pool)
		ociRepo = pgOCIRepo
		if blossomClient == nil {
			return nil, fmt.Errorf("OCI registry requires Blossom servers to be configured")
		}
		ociSvc, err = service.NewOCIRegistryService(cfg.OCI, pgOCIRepo, pgOCIRepo, blossomClient, logger)
		if err != nil {
			return nil, fmt.Errorf("create oci registry service: %w", err)
		}
		nip98Validator := auth.NewNIP98Validator(auth.DefaultNIP98Config(), auth.NewPGNIP98ReplayStore(pool))
		ociHandler = handlers.NewOCIRegistryHandler(ociSvc, nip98Validator, cfg.OCI)
		bgManager.RegisterWithOptions(NewOCIUploadCleanupRunner(ociSvc, cfg.OCI.UploadExpiry, logger))
		logger.Info("oci registry enabled", zap.String("host", cfg.OCI.PublicHost))
	}

	// Hive-CI wiring.
	var buildResultRegistrar controlplane.BuildResultArtifactRegistrar
	// The initiator consults ingested runs so a build/request adopts an existing
	// trusted 5401 for the same (a, commit, workflow) instead of competing.
	var hiveRunLookup giteaAdapter.WorkflowRunLookup
	if shouldRegisterHiveCIRunners(cfg.HiveCI) {
		hiveRepo := repository.NewPgHiveCIRepository(pool)
		hiveRunLookup = hiveRepo
		bridge := pipeline.NewBridge(
			hiveRepo, serviceRepo, buildRepo, artifactRepo, intentRepo, envRepo,
			ociRepo, pipelineRegistryInspector, registry,
			cfg.HiveCI.TrustedCIPubkeys, cfg.HiveCI.AutoRegisterBuilds, logger,
		)
		if relayFirstRegistry != nil {
			bridge = pipeline.NewBridge(
				hiveRepo, serviceRepo, buildRepo, artifactRepo, intentRepo, envRepo,
				ociRepo, pipelineRegistryInspector, relayFirstRegistry,
				cfg.HiveCI.TrustedCIPubkeys, cfg.HiveCI.AutoRegisterBuilds, logger,
			)
		}
		bridge.SetTrustedResultPubkeys(cfg.HiveCI.TrustedLoomWorkerPubkeys)
		if runtimeLifecycleSvc != nil {
			bridge.SetDesiredStateBuilder(runtimeLifecycleSvc)
		}
		buildResultRegistrar = bridge
		// Wrap bridge.ProcessResult to match the ResultConsumer signature (no error return).
		onResult := func(ctx context.Context, resultEventID string) {
			if err := bridge.ProcessResult(ctx, resultEventID); err != nil {
				logger.Error("bridge process result failed", zap.String("result_event_id", resultEventID), zap.Error(err))
			}
		}
		hiveSub := hiveciAdapter.NewSubscriber(relayPool, hiveRepo, cfg.HiveCI.TrustedCIPubkeys, logger, onResult)
		hiveSub.SetTrustedResultPubkeys(cfg.HiveCI.TrustedLoomWorkerPubkeys)
		if len(cfg.HiveCI.TrustedLoomWorkerPubkeys) == 0 {
			logger.Warn("Hive-CI Loom worker result allowlist is empty; worker-signed 5402 artifact results will be rejected",
				zap.String("reason", "trusted_loom_worker_pubkeys_missing"))
		}
		if len(cfg.HiveCI.TrustedReleaseAttestors) > 0 {
			if ociSvc == nil {
				return nil, fmt.Errorf("Hive-CI release registration requires Bahia OCI registry evidence resolution")
			}
			if controlPlaneSigner == nil {
				return nil, fmt.Errorf("Hive-CI release registration requires a control-plane audit signer")
			}
			releaseAudit := hiveciAdapter.NewRegistrationAudit(controlPlaneSigner, auditEventRepo)
			releaseEvidence := hiveciAdapter.NewRepositoryReleaseEvidence(
				nostrEventRepo, hiveRepo, workerRepo, hiveciAdapter.NewOCIReleaseObjectResolver(ociSvc, pipelineRegistryInspector),
			)
			releaseIngestor := hiveciAdapter.NewReleaseIngestor(
				releaseEvidence, hiveRepo, cfg.HiveCI.TrustedReleaseAttestors, cfg.HiveCI.TrustedCIPubkeys,
			)
			promotionSvc, err := service.NewProductionAgentRuntimePromotionService(
				agentRuntimeReleaseRepo, serviceRepo, envRepo, hiveRepo, registry,
			)
			if err != nil {
				return nil, fmt.Errorf("configure accepted Hive-CI runtime promotion: %w", err)
			}
			bridge.SetReleaseRegistrationAuditor(releaseAudit)
			hiveSub.SetReleaseAuditor(releaseAudit)
			hiveSub.SetReleaseEvidenceRecorder(nostrEventRepo)
			hiveSub.SetReleaseAttestors(cfg.HiveCI.TrustedReleaseAttestors)
			hiveSub.SetReleaseIngestor(releaseIngestor, func(ctx context.Context, commit domain.HiveCIReleaseCommitResult) {
				if _, err := bridge.RegisterAcceptedRelease(ctx, commit.Release); err != nil {
					logger.Error("register accepted Hive-CI release artifact failed",
						zap.String("release_identity", commit.Release.Result.ReleaseIdentity),
						zap.Bool("replay", commit.Replay), zap.Error(err))
					return
				}
				if _, err := promotionSvc.PromoteAcceptedHiveCIRelease(ctx, commit); err != nil {
					logger.Error("promote accepted Hive-CI runtime release failed",
						zap.String("release_identity", commit.Release.Result.ReleaseIdentity),
						zap.Bool("replay", commit.Replay), zap.Error(err))
				}
			})
		}
		var dependencyPinner hiveCIDependencyPinner
		if hiveCIHasBuildDependencies(cfg.HiveCI.Policies) {
			baseURL, token := cfg.HiveCI.DependencyGiteaEndpoint()
			dependencyResolver, err := giteaAdapter.NewBuildDependencyResolver(
				giteaAdapter.NewAPIClient(baseURL, token, nil), baseURL,
			)
			if err != nil {
				return nil, fmt.Errorf("configure Hive-CI build dependency resolver: %w", err)
			}
			dependencyPinner = dependencyResolver
		}
		runDispatcher := newHiveCIRunDispatcher(cfg.HiveCI.Policies, dependencyPinner, loomClient, logger)
		hiveSub.SetRunConsumer(runDispatcher.Dispatch)
		bgManager.RegisterWithOptions(hiveSub)
		bgManager.RegisterWithOptions(NewHiveCIRetryRunner(hiveRepo, bridge, cfg.HiveCI.RetryInterval, cfg.HiveCI.MaxRetries, logger))

		// Seed configured pipeline policies idempotently.
		for i, pc := range cfg.HiveCI.Policies {
			if pc.RepoCoordinate == "" || pc.WorkflowPath == "" || pc.ServiceName == "" || pc.EnvironmentName == "" {
				logger.Warn("skipping incomplete hiveci policy config",
					zap.Int("index", i),
					zap.String("repo_coordinate", pc.RepoCoordinate),
					zap.String("workflow_path", pc.WorkflowPath),
					zap.String("service_name", pc.ServiceName),
					zap.String("environment_name", pc.EnvironmentName),
				)
				continue
			}
			svc, err := serviceRepo.GetByName(ctx, pc.ServiceName)
			if err != nil || svc == nil {
				logger.Warn("hiveci policy: service not found, skipping",
					zap.String("service_name", pc.ServiceName), zap.Error(err))
				continue
			}
			env, err := envRepo.GetByName(ctx, pc.EnvironmentName)
			if err != nil || env == nil {
				logger.Warn("hiveci policy: environment not found, skipping",
					zap.String("environment_name", pc.EnvironmentName), zap.Error(err))
				continue
			}
			enabled := true
			if pc.Enabled != nil {
				enabled = *pc.Enabled
			}
			policy := domain.HiveCIPipelinePolicy{
				RepoCoordinate: pc.RepoCoordinate,
				WorkflowPath:   pc.WorkflowPath,
				BranchPattern:  pc.BranchPattern,
				ServiceID:      svc.ID,
				EnvironmentID:  env.ID,
				Enabled:        enabled,
				Metadata:       pc.Metadata,
			}
			if err := hiveRepo.EnsurePipelinePolicy(ctx, policy); err != nil {
				logger.Error("failed to ensure hiveci pipeline policy",
					zap.String("repo_coordinate", pc.RepoCoordinate),
					zap.String("workflow_path", pc.WorkflowPath),
					zap.Error(err),
				)
			} else {
				logger.Info("hiveci pipeline policy ensured",
					zap.String("repo_coordinate", pc.RepoCoordinate),
					zap.String("workflow_path", pc.WorkflowPath),
					zap.String("service", pc.ServiceName),
					zap.String("environment", pc.EnvironmentName),
				)
			}
		}

		logger.Info("hive-ci bridge enabled",
			zap.Strings("subscription_relays", relayURLs),
			zap.Int("trusted_ci_pubkeys", len(cfg.HiveCI.TrustedCIPubkeys)),
			zap.Int("trusted_loom_worker_pubkeys", len(cfg.HiveCI.TrustedLoomWorkerPubkeys)),
			zap.Int("trusted_release_attestors", len(cfg.HiveCI.TrustedReleaseAttestors)),
			zap.Bool("auto_register_builds", cfg.HiveCI.AutoRegisterBuilds),
			zap.Int("pipeline_policies", len(cfg.HiveCI.Policies)))
	} else {
		logger.Warn("Hive-CI release ingestion is disabled; signed 5401/5402 events will not be consumed",
			zap.String("reason", "hiveci_disabled"), zap.Strings("available_interop_relays", relayURLs))
	}

	var securityScanner *service.SecurityScanner
	if securityRepo != nil && sbomStorageResolver != nil && nostrPub != nil && relayPool != nil {
		// bahia-irsry.60: confidential cp-state for security findings.
		var securityCPPub *nostrAdapter.SecurityCanonicalPublisher
		if nostrProjector != nil && confidentialEncryptor != nil {
			securityCPPub = nostrAdapter.NewSecurityCanonicalPublisher(nostrProjector, confidentialEncryptor, logger)
		}
		securityScanner = service.NewSecurityScanner(service.SecurityScannerConfig{
			Repo:       securityRepo,
			SBOMs:      sbomManifestRepo,
			Policies:   policySvc,
			Events:     publisher,
			Storage:    sbomStorageResolver,
			OSV:        securityAdapter.NewOSVClient(),
			Publisher:  sbomPublishAdapter{publisher: nostrPub},
			Subscriber: securityRelaySubscriber{pool: relayPool},
			Pubkey:     servicePubkey,
			Logger:     logger,
		})
		// Publications recorded as queued become published when the outbox
		// delivers their event, and failed_terminal if it abandons it.
		nostrPub.OnDeliveryAbandoned(securityScanner.HandlePublishAbandoned)
		nostrPub.OnDelivered(securityScanner.HandlePublishDelivered)
		bgManager.RegisterWithOptions(securityScanner)
		bgManager.RegisterWithOptions(service.NewSecurityScheduler(service.SecuritySchedulerConfig{Repo: securityRepo, Scanner: securityScanner, Deriver: policySvc, Logger: logger}))
		// bahia-irsry.60: wire schedule cp-state publisher to policy service.
		if securityCPPub != nil {
			policySvc.SetSecurityScheduleCPPublisher(securityCPPub)
		}
		logger.Info("security OSV scanner and scheduler registered")
	}

	// Payment service exposes payment records and history; estimates use relay-backed worker pricing.
	// It does not create or redeem Cashu tokens; cashu.enabled live wallet mode
	// remains fail-closed until mint-backed proof flows are implemented.
	paymentSvc := service.NewPaymentService(paymentRepo, logger)
	// bahia-irsry.60: confidential cp-state for payment records.
	if nostrProjector != nil && confidentialEncryptor != nil {
		paymentCanonical := nostrAdapter.NewPaymentCanonicalPublisher(nostrProjector, confidentialEncryptor, logger)
		paymentSvc.SetCPStatePublisher(paymentCanonical)
		logger.Info("payment cp-state publisher wired")
	}
	if cfg.Cashu.Enabled {
		return nil, fmt.Errorf("cashu.enabled=true is unsupported because mint-backed token flows are not implemented; disable cashu.enabled")
	}

	// Notification system.
	notifRepo := nostrAdapter.NewCanonicalNotificationRepository(repository.NewPgNotificationRepository(pool), f74bCanonical)
	notifDispatcher := notifications.NewDispatcher(notifRepo, logger)
	notifDispatcher.RegisterSender(domain.ChannelTypeWebhook, notifications.NewWebhookSender())
	if cfg.Nostr.PrivateKey != "" {
		notifDispatcher.RegisterSender(domain.ChannelTypeNostrDM,
			notifications.NewNostrDMSender(relayPool, cfg.Nostr.PrivateKey, logger))
	}
	notifDispatcher.SetupSubscriptions(publisher)

	// --- Phase 3 N1: Secret and notification intent handlers ---
	// Sensitive domains whose intents arrive as NIP-59 gift wraps (kind 1059)
	// through the shared gift-wrapped intent ingress (O1; SensitiveDomains
	// includes "secret" and "notification").
	// SecretCanonicalPublisher follows the BackupCanonicalPublisher pattern:
	// holds a *Projector reference and publishes through the shared signing/outbox
	// pipeline. Secret values are NEVER included in published events.
	secretOrgResolver := controlplane.NewServiceBackedSecretOrgResolver(serviceRepo, secretRepo)
	secretCanonical := nostrAdapter.NewSecretCanonicalPublisher(nostrProjector, confidentialEncryptor, secretOrgResolver, logger)

	// Register the secret intent handler when the secret domain is enabled.
	if enabledDomains["secret"] && secretRepo != nil {
		intentProcessor.RegisterHandler("secret", controlplane.NewSecretIntentHandler(
			controlplane.SecretIntentHandlerConfig{
				Registry:  secretRepo,
				Encryptor: secretEncryptor,
				Publisher: secretCanonical,
				Status:    intentStatus,
				Logger:    logger,
			},
		))
		logger.Info("secret intent handler registered")
	}

	// NotificationCanonicalPublisher strips sensitive fields (webhook URLs,
	// secrets, credentials) from published content.
	notifOrgResolver := controlplane.NewRepoBackedNotificationOrgResolver(notifRepo)
	notifCanonical := nostrAdapter.NewNotificationCanonicalPublisher(nostrProjector, confidentialEncryptor, notifOrgResolver, logger)

	// Register the notification intent handler when the notification domain is
	// enabled. The dispatcher's OnChannelChanged method is the event-driven
	// notifier that replaces DB polling (Phase 3 N1).
	if enabledDomains["notification"] && notifRepo != nil {
		intentProcessor.RegisterHandler("notification", controlplane.NewNotificationIntentHandler(
			controlplane.NotificationIntentHandlerConfig{
				Registry:  notifRepo,
				Publisher: notifCanonical,
				Notifier:  notifDispatcher,
				Status:    intentStatus,
				Logger:    logger,
			},
		))
		logger.Info("notification intent handler registered")
	}
	// --- end N1 wiring ---

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
	// Explicit recovery for stranded stored intents; newly arrived tool requests
	// enter through ContextVM and are handled by the event-driven transport path.
	bgManager.RegisterWithOptions(toolCoordinator)

	// MCP (Model Context Protocol) server for AI agent integration.
	var mlCommandPublisher mcp.MLCommandPublisher
	if mlRegistry != nil && controlPlaneSigner != nil && controlPlanePool != nil && len(controlPlaneRelays) > 0 {
		mlCommandPublisher = controlplane.NewMLCommandPublisher(controlPlanePool, controlPlaneSigner)
	}

	// Fleet hygiene (Swabbie, fp-jan): periodic dry-run scans + Tier-1
	// convergence via the per-host maintenance driver.
	var hygieneObservationSource *reconcile.ContextVMHygieneObservationSource
	if cfg.Hygiene.Enabled {
		if controlPlaneSigner == nil || controlPlanePool == nil || len(controlPlaneRelays) == 0 {
			logger.Warn("hygiene reconciler enabled but control-plane Nostr publishing is not configured; skipping")
		} else if hygienePolicy, err := loadHygienePolicy(cfg.Hygiene.PolicyPath); err != nil {
			return nil, fmt.Errorf("load hygiene policy: %w", err)
		} else {
			hygieneObservationSource, err = reconcile.NewContextVMHygieneObservationSource(servicePubkey, logger)
			if err != nil {
				return nil, fmt.Errorf("hygiene observation source: %w", err)
			}
			maintenancePublisher := controlplane.NewMaintenanceCommandPublisher(controlPlanePool, controlPlaneSigner, hygieneObservationSource)
			hygieneReconciler, err := reconcile.NewHygieneReconciler(hygienePolicy, cfg.Hygiene.Workers, maintenancePublisher, hygieneObservationSource, telemetryProvider.GetMetrics(), cfg.Hygiene.Interval, publisher, logger)
			if err != nil {
				return nil, fmt.Errorf("hygiene reconciler: %w", err)
			}
			bgManager.RegisterWithOptions(hygieneReconciler, RunnerRequired(false))
		}
	}
	mcpDeps := mcp.ServerDeps{
		IntentProcessor:    intentProcessor,
		StateStore:         localEventStore,
		ServicePubkey:      servicePubkey,
		ConfidentialReader: confidentialEncryptor,
		LogService:         runLogService,
		SBOMs:              sbomRepo,
		Signatures:         sigRepo,
		SignVerifier:       signVerifier,
		MLCommandPublisher: mlCommandPublisher,
		LLMRegistry:        llmRegistry,
	}
	configureToolApprovalMCPDeps(&mcpDeps, controlPlanePool, controlPlaneSigner, controlPlaneRelays)
	configureAuthorizationMCPDeps(&mcpDeps, cfg, tenantRBAC)
	mcpServer, err := mcp.NewServerWithOptionsChecked(registry, logger, mcpDeps)
	if err != nil {
		return nil, fmt.Errorf("initialize MCP server: %w", err)
	}
	mcpHandler := handlers.NewMCPHandler(mcpServer, logger)
	logger.Info("mcp server initialized")

	var assistantOrchestrator *service.AssistantOrchestrator
	var assistantIdentity service.AssistantIdentity
	var configFabricSigner service.ConfigFabricSigner
	if cfg.Assistant.Enabled {
		identity, assistantSignetManager, assistantBootstrapRunner, operatorSigner := bootstrapOperatorAssistant(cfg, controlPlaneRelays, logger)
		configFabricSigner = operatorSigner
		if assistantSignetManager != nil {
			bgManager.RegisterWithOptions(assistantSignetManager, RunnerRequired(false))
			registerSignetHealthCheck(healthProvider, assistantSignetManager)
		}
		if assistantBootstrapRunner != nil {
			bgManager.RegisterWithOptions(assistantBootstrapRunner, RunnerRequired(false))
		}
		assistantIdentity = identity
		var assistantDNS service.AssistantDNSRegistry
		if dnsProjector != nil {
			assistantDNS = assistantDNSRegistryAdapter{endpoints: dnsProjector, zones: dnsZoneRepo, staticZones: dnsZones, policies: dnsPolicyRepo}
		}
		assistantPublisher := &auditedNostrPublisher{delegate: controlPlanePool, repo: nostrEventRepo, logger: logger}
		assistantSubscriber := assistantRelaySubscriber{pool: controlPlanePool}
		servicePubkey := ""
		if strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
			if secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey)); err == nil {
				servicePubkey = secret.Public().Hex()
			}
		}
		transcriptKeys, err := assistantTranscriptKeyProvider(cfg)
		if err != nil {
			return nil, err
		}
		transcriptStore := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{
			Publisher:     assistantPublisher,
			Subscriber:    assistantSubscriber,
			Signer:        controlPlaneSigner,
			Identity:      identity,
			KeyProvider:   transcriptKeys,
			ServicePubkey: servicePubkey,
		})
		userDocs := docs.New(docs.DefaultBasePath)
		contextBuilder := service.NewAssistantContextBuilder(registry, llmRegistry, mlRegistry, assistantDNS, nil, &userDocs, service.AssistantContextBuilderConfig{TranscriptHistory: transcriptStore})
		// The batch proposer's chat client exists only when assistant.llm_model
		// is set; without it the batch workflow is unavailable (see wiring).
		var chatClient service.AssistantChatClient
		if cfg.Assistant.BatchWorkflowAvailable() {
			chatClient = llmadapter.NewChatClient(llmadapter.ChatClientConfig{
				BaseURL: cfg.Assistant.LLMBaseURL,
				Model:   cfg.Assistant.LLMModel,
				APIKey:  cfg.Assistant.LLMAPIKey,
			}, slog.Default())
		}
		modelClient, modelClientErr := newAssistantAgentModelClient(cfg.Assistant.Agentic, slog.Default())
		if modelClientErr != nil {
			return nil, modelClientErr
		}
		externalMCP, externalErr := loadAssistantExternalMCP(ctx, cfg.Assistant.MCP.ExternalServers, slog.Default())
		if externalErr != nil {
			return nil, externalErr
		}
		assistantExecution, err := buildAssistantExecution(assistantExecutionDeps{
			Config:           cfg,
			MCPServer:        mcpServer,
			ContextBuilder:   contextBuilder,
			ChatClient:       chatClient,
			ModelClient:      modelClient,
			Publisher:        assistantPublisher,
			Subscriber:       assistantSubscriber,
			Signer:           controlPlaneSigner,
			Identity:         identity,
			ServicePubkey:    servicePubkey,
			Transcript:       transcriptStore,
			KeyProvider:      transcriptKeys,
			InitialSessions:  loadAssistantSessions(ctx, nostrEventRepo, logger),
			ExternalMCP:      externalMCP,
			RelayConnections: controlPlanePool,
			History:          projectionHistory,
		})
		if err != nil {
			return nil, err
		}
		assistantOrchestrator = assistantExecution.Orchestrator
		bgManager.RegisterWithOptions(assistantExecution.Lifecycle, RunnerRequired(false))
		bgManager.RegisterWithOptions(assistantExecution.Recovery, RunnerRequired(false))
		if assistantExecution.Healer != nil {
			bgManager.RegisterWithOptions(assistantExecution.Healer, RunnerRequired(false))
		}
		availableWorkflows := make([]string, 0, len(assistantExecution.AvailableWorkflows))
		for _, workflow := range assistantExecution.AvailableWorkflows {
			availableWorkflows = append(availableWorkflows, string(workflow))
		}
		logFields := []zap.Field{zap.String("agent_id", identity.AgentID), zap.String("assistant_pubkey", identity.Pubkey), zap.String("default_workflow", string(assistantExecution.DefaultWorkflow)), zap.Strings("available_workflows", availableWorkflows)}
		if assistantExecution.Batch == nil {
			logFields = append(logFields, zap.String("batch_unavailable_reason", assistantBatchUnavailableReason))
		}
		logger.Info("operator assistant executor initialized", logFields...)
	}

	configFabricSvc := service.NewConfigFabricService(nostrEventRepo, configFabricPublishAdapter{publisher: controlPlanePub}, configFabricSigner,
		service.WithDeliveryQuery(controlPlanePub))

	// Nostr inbound subscriber: listens for Hive-CI, Loom, and Bahia events.
	nostrSub := nostrAdapter.NewSubscriber(relayPool, pgNostrEventRepo, logger,
		nostrAdapter.WithLocalStore(localEventStore),
		nostrAdapter.WithSelfAuthors(servicePubkey),
		nostrAdapter.WithInboundSync(inboundSyncConfigScoped(cfg.Nostr.LocalStore, cfg.Nostr.ServiceRelays)),
		// NIP-09 deletions from the control-plane authors reach the
		// projection cache live, as the bootstrapper's deletion group does.
		nostrAdapter.WithDeletionAuthors(controlPlaneAuthors),
		nostrAdapter.WithObserver(bootstrapper.ApplyDeletion),
		nostrAdapter.WithHandler(nostrProcessor.Handle),
		nostrAdapter.WithObserver(telemetryProvider.ObserveNostrEvent),
		nostrAdapter.WithIngestionObserver(telemetryProvider),
		nostrAdapter.WithAuthorizedAuthorScopes(controlPlaneSubscriberAuthorScopes(cfg, assistantIdentity)),
	)
	bgManager.RegisterWithOptions(nostrSub)

	// NIP-23 docs publisher: syncs user-guide documentation to the sidecar relay
	// (or control-plane relays) as long-form content. Uses controlPlanePool so
	// docs land on the same relay set the browser reads from.
	if controlPlanePool != nil && cfg.Nostr.PublishEnabled && cfg.Nostr.PrivateKey != "" {
		userDocsForNostr := docs.New(docs.DefaultBasePath)
		var docsQuerier docs.NostrDocsQuerier
		if servicePubkey != "" {
			docsQuerier = newDocsRelayQuerier(controlPlanePool, servicePubkey, logger)
		}
		docsNostrPublisher := docs.NewNostrDocsPublisher(userDocsForNostr, controlPlanePub, docsQuerier, logger)
		bgManager.RegisterWithOptions(docsNostrPublisher, RunnerRequired(false))
		logger.Info("NIP-23 docs publisher registered", zap.Strings("relays", controlPlaneRelays))
	}

	if servicePubkey != "" && relayPolicyProjectionRepo != nil {
		relayTopologyCoordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
			ControlPlanePool:      controlPlanePool,
			ContextVMRequestPool:  contextVMRequestPool,
			ContextVMResponsePool: contextVMResponsePool,
			ServicePool:           relayPool,
			NostrConfig:           cfg.Nostr,
			LoomRelays:            cfg.Loom.Relays,
			Logger:                logger,
		})
		relaySettingsHydrator := controlplane.NewRelaySettingsHydrator(controlplane.RelaySettingsHydratorConfig{
			Pool:            relayPolicyHydrationPool,
			ServicePubkey:   servicePubkey,
			ProjectionStore: relayPolicyProjectionRepo,
			Logger:          logger,
			OnSnapshotApplied: func(applyCtx context.Context, state controlplane.RelayPolicyState) error {
				if err := relayTopologyCoordinator.ApplySnapshot(applyCtx, state); err != nil {
					return err
				}
				relayPolicyHydrationPool.ReconfigureRelayURLsContext(
					applyCtx,
					relayPolicyHydrationRelayURLsForState(relayPolicyHydrationRelays, state),
				)
				return nil
			},
		})
		if err := relaySettingsHydrator.LoadProjection(ctx); err != nil {
			return nil, fmt.Errorf("loading durable relay settings projection before control-plane activation: %w", err)
		}
		healthProvider.RegisterCheck("relay_policy_projection", func() HealthCheck {
			projection, projected := relaySettingsHydrator.Projection()
			_, hydrated := relaySettingsHydrator.Snapshot()
			check := HealthCheck{
				Name:    "relay_policy_projection",
				Status:  HealthStatusPass,
				Message: "no validated relay policy has been observed; Nostr convergence remains asynchronous",
				Details: map[string]string{"availability": "unavailable"},
			}
			if !projected || !hydrated {
				return check
			}
			confirmation := "cached"
			if projection.RelayConfirmedAt != nil {
				confirmation = "relay_confirmed"
			}
			check.Message = "validated relay policy projection is hydrated"
			check.Details = map[string]string{
				"availability":     "available",
				"event_id":         projection.EventID,
				"hash":             projection.PayloadHash,
				"author":           projection.AuthorPubkey,
				"event_created_at": projection.EventCreatedAt.UTC().Format(time.RFC3339Nano),
				"confirmation":     confirmation,
			}
			return check
		})
		bgManager.RegisterWithOptions(relaySettingsHydrator)
		logger.Info("durable relay settings projection hydrator registered",
			zap.Int("eligible_relay_count", len(relayPolicyHydrationPool.URLs())),
			zap.String("service_pubkey", servicePubkey),
		)
	}

	releasePromotionAudit := controlplane.NewSignedReleasePromotionAudit(controlPlaneSigner, auditEventRepo)
	releasePromotionAuthorizer := controlplane.NewReleasePromotionAuthorizer(registry, releasePromotionAudit)
	serviceDeploymentConfig := controlplane.EncryptedServiceHandlersConfig{
		Registry:          registry,
		RuntimeLifecycle:  runtimeLifecycleSvc,
		Policy:            policySvc,
		PublicRoutes:      publicRoutePlanner,
		Services:          serviceRepo,
		DeploymentUnits:   deploymentUnitRepo,
		RBAC:              tenantRBAC,
		ReleasePromotions: releasePromotionAuthorizer,
		Logger:            logger,
		IntentProcessor:   intentProcessor,
	}
	if enabledDomains["deployment"] || enabledDomains["runtime"] {
		deploymentHandler := controlplane.NewDeploymentIntentHandler(serviceDeploymentConfig, runtimeLifecycleSvc)
		if enabledDomains["deployment"] {
			intentProcessor.RegisterHandler("deployment", deploymentHandler)
		}
		if enabledDomains["runtime"] {
			intentProcessor.RegisterHandler("runtime", deploymentHandler)
		}
	}

	var encryptedRequestTransport *controlplane.EncryptedRequestTransport
	// Encrypted request/result event runtime for sensitive browser route migrations.
	if len(contextVMRequestRelays) > 0 && controlPlaneSigner != nil && cfg.Nostr.PrivateKey != "" {
		responder := controlplane.NewEncryptedResponder(contextVMResponsePool, controlPlaneSigner, cfg.Nostr.PrivateKey, logger)
		transportOptions := []controlplane.EncryptedRequestTransportOption{
			controlplane.WithContextVMLocalStore(localEventStore),
			controlplane.WithContextVMLocalStoreConfig(controlplane.ContextVMLocalConfig{
				RequestMaxAge:       cfg.Nostr.LocalStore.RequestMaxAge,
				WrapBackdateOverlap: cfg.Nostr.LocalStore.WrapBackdateOverlap,
			}),
		}
		if contextVMResponseStore != nil {
			transportOptions = append(transportOptions, controlplane.WithContextVMResponseStore(contextVMResponseStore, defaultContextVMResponseRetention))
		}
		encryptedRequestTransport = controlplane.NewEncryptedRequestTransport(contextVMRequestPool, responder, cfg.Nostr.AuthorizedPubkeys, logger, transportOptions...)
		virtualization.Handlers.Register(encryptedRequestTransport)
		fleetOperatorGate := controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys)
		if hygieneObservationSource != nil {
			encryptedRequestTransport.RegisterContextVMResponseHandler(hygieneObservationSource.HandleContextVMResponse)
		}
		controlplane.NewEncryptedDomainHandlers(controlplane.EncryptedDomainHandlersConfig{
			Payments:              paymentSvc,
			Orgs:                  orgRepo,
			Members:               orgMemberRepo,
			Invites:               orgInviteRepo,
			RBAC:                  tenantRBAC,
			IntentProcessor:       intentProcessor,
			OrgPublisher:          orgCanonicalPub,
			BootstrapOwnerPubkeys: cfg.Auth.BootstrapOwnerPubkeys,
			Logger:                logger,
		}).Register(encryptedRequestTransport)
		// Phase 3 O1: wire gift-wrap intent ingress to the existing 1059
		// subscription. When an unwrapped inner event is kind 30900 with
		// t=bahia-intent, it is routed to the ingress instead of ContextVM.
		if giftWrapIngress != nil {
			encryptedRequestTransport.SetGiftWrapIntentIngress(giftWrapIngress)
			logger.Info("gift-wrap intent ingress wired to ContextVM transport")
		}
		registryMutations := controlplane.RegistryMutationBackend(registry)
		if relayFirstRegistry != nil {
			registryMutations = relayFirstRegistry
		}
		controlplane.NewEncryptedRouteHandlers(controlplane.EncryptedRouteHandlersConfig{
			IntentProcessor: intentProcessor,
			Secrets:         secretRepo,
			Encryptor:       secretEncryptor,
			SecretPublisher: secretCanonical,
			NotifRepo:       notifRepo,
			NotifPublisher:  notifCanonical,
			NotifNotifier:   notifDispatcher,
			Runs:            runRepo,
			RunLogs:         runLogService,
			Artifacts:       artifactRepo,
			Signatures:      sigRepo,
			SignVerifier:    signVerifier,
			Services:        serviceRepo,
			Intents:         intentRepo,
			Registry:        registryMutations,
			DeploymentUnits: deploymentUnitRepo,
			RBAC:            tenantRBAC,
			Logger:          logger,
		}).Register(encryptedRequestTransport)
		// The build request contract is registered even while the fleet Gitea
		// mirror initiator is unavailable, so browsers receive a signed,
		// fail-closed error instead of falling back to credential-bearing flows.
		var hiveCIBuildStarter controlplane.HiveCIBuildStarter
		if cfg.HiveCI.Initiator.Enabled && secretEncryptor != nil {
			dependencyAuthorizations, err := hiveCIBuildDependencyAuthorizations(ctx, cfg.HiveCI.Policies, serviceRepo)
			if err != nil {
				return nil, fmt.Errorf("configure Hive-CI service build dependencies: %w", err)
			}
			hiveCIInitiator = giteaAdapter.NewInitiator(
				giteaAdapter.NewAPIClient(cfg.HiveCI.Initiator.GiteaBaseURL, cfg.HiveCI.Initiator.GiteaToken, nil),
				secretsAdapter.NewResolver(secretRepo, secretEncryptor),
				controlPlanePool,
				controlPlaneSigner,
				giteaAdapter.NewPgInitiationStore(pool, secretEncryptor),
				giteaAdapter.InitiatorConfig{
					GiteaBaseURL:                  cfg.HiveCI.Initiator.GiteaBaseURL,
					MirrorOwner:                   cfg.HiveCI.Initiator.MirrorOwner,
					WorkflowPath:                  cfg.HiveCI.Initiator.WorkflowPath,
					SourceProvider:                cfg.HiveCI.Initiator.SourceProvider,
					SourceCloneURL:                cfg.HiveCI.Initiator.SourceCloneURL,
					SourceAuthUsername:            cfg.HiveCI.Initiator.SourceAuthUsername,
					MirrorReadUsername:            cfg.HiveCI.Initiator.MirrorReadUsername,
					MirrorReadCredentialRef:       cfg.HiveCI.Initiator.MirrorReadCredentialRef,
					RepoAnnouncementAddr:          cfg.HiveCI.Initiator.RepoAnnouncementAddr,
					TrustedCIPubkeys:              cfg.HiveCI.TrustedCIPubkeys,
					TrustedLoomWorkerPubkeys:      cfg.HiveCI.TrustedLoomWorkerPubkeys,
					BuildDependencyAuthorizations: dependencyAuthorizations,
					RelayHint:                     cfg.HiveCI.Initiator.RelayHint,
				},
				logger,
				giteaAdapter.WithLoomJobSubmitter(hiveCIJobClient),
				giteaAdapter.WithWorkflowRunLookup(hiveRunLookup),
				giteaAdapter.WithPublicationInspector(giteaAdapter.NewRelayPublicationInspector(controlPlanePool)),
			)
			hiveCIBuildStarter = hiveCIInitiator
			logger.Info("fleet gitea private-mirror HiveCI build initiator enabled",
				zap.String("gitea_base_url", cfg.HiveCI.Initiator.GiteaBaseURL),
				zap.String("mirror_owner", cfg.HiveCI.Initiator.MirrorOwner),
				zap.String("workflow_path", cfg.HiveCI.Initiator.WorkflowPath),
				zap.String("source_provider", cfg.HiveCI.Initiator.SourceProvider),
			)
		}
		buildHandlers := controlplane.NewEncryptedBuildHandlers(controlplane.EncryptedBuildHandlersConfig{
			Starter:           hiveCIBuildStarter,
			Registry:          registry,
			Builds:            buildRepo,
			ArtifactRegistrar: buildResultRegistrar,
			Services:          serviceRepo,
			Secrets:           secretRepo,
			RBAC:              tenantRBAC,
			IntentProcessor:   intentProcessor,
		})
		if enabledDomains["build"] {
			intentProcessor.RegisterHandler("build", controlplane.NewBuildIntentHandler(buildHandlers))
		}
		buildHandlers.Register(encryptedRequestTransport)
		controlplane.NewOperatorContextVMHandlers(controlplane.OperatorContextVMHandlersConfig{
			Adoption:                       adoptionSvc,
			RuntimeLifecycle:               runtimeLifecycleSvc,
			AdoptionAuthorizedPubkeys:      cfg.Adoption.AllowedPubkeys,
			DirectRuntimeAuthorizedPubkeys: cfg.DirectRuntime.AllowedPubkeys,
			IntentProcessor:                intentProcessor,
			Resources:                      registry,
		}).Register(encryptedRequestTransport)
		controlplane.RegisterWorkerContextVMHandlers(encryptedRequestTransport, fleetOperatorGate, intentProcessor)
		bgManager.RegisterWithOptions(controlplane.RegisterContinuityContextVMHandlers(encryptedRequestTransport, fleetOperatorGate, controlPlanePool, continuityDefinitionStore, continuityRecipeExecutor, logger))
		controlplane.RegisterBackupAliasContextVMHandlers(encryptedRequestTransport, tenantRBAC, fleetOperatorGate, intentProcessor)
		controlplane.RegisterLoomContextVMHandlers(encryptedRequestTransport, loomClient, cfg.Loom.AuthorizedPubkeys, fleetOperatorGate)
		controlplane.RegisterDNSContextVMHandlers(encryptedRequestTransport, dnsOperator, cfg.DNS.Enabled, fleetOperatorGate, intentProcessor)
		controlplane.RegisterMLRegistryContextVMHandlers(encryptedRequestTransport, mlRegistry, fleetOperatorGate, intentProcessor, registry)
		controlplane.RegisterNotificationEncryptedHandlers(encryptedRequestTransport, notifRepo, notifDispatcher, tenantRBAC)
		relayAdminClient := buildRelayAdminClient(ctx, cfg, secretRepo, secretEncryptor, logger)
		controlplane.RegisterRelaySettingsContextVMHandlers(encryptedRequestTransport, controlplane.RelaySettingsHandlerConfig{
			Config:            cfg,
			AdminClient:       relayAdminClient,
			ProjectionStore:   relayPolicyProjectionRepo,
			ServicePubkey:     servicePubkey,
			Logger:            logger,
			ConfigFabric:      configFabricSvc,
			FleetOperatorGate: fleetOperatorGate,
		})
		controlplane.RegisterAssistantContextVMHandlers(encryptedRequestTransport, assistantOrchestrator, fleetOperatorGate)
		controlplane.RegisterServiceContextVMHandlers(encryptedRequestTransport, serviceDeploymentConfig)
		if sbomOrchestrator != nil {
			sbomAsyncRunner := service.NewSBOMAsyncRunner(sbomOrchestrator)
			controlplane.RegisterSBOMContextVMHandlers(encryptedRequestTransport, sbomAsyncRunner, fleetOperatorGate)
			bgManager.RegisterWithOptions(sbomAsyncRunner)
		}
		controlplane.RegisterSecurityContextVMHandlers(encryptedRequestTransport, securityScanner, fleetOperatorGate)
		soulfactory.RegisterContextVMHandlers(encryptedRequestTransport, soulFactoryReactorFromRuntime(soulFactoryRuntime))
		soulfactory.RegisterSagaContextVMHandlers(encryptedRequestTransport, soulFactoryReactorFromRuntime(soulFactoryRuntime), fleetOperatorGate)
		// ContextVM carries the canonical mutation plane, so it must remain
		// available in the minimum production control-plane tier.
		bgManager.RegisterWithOptions(&encryptedRequestTransportRunner{transport: encryptedRequestTransport})
		logger.Info("encrypted request/result event runtime registered",
			zap.Strings("request_subscription_relays", contextVMRequestRelays),
			zap.Strings("response_publication_relays", contextVMResponseRelays),
		)
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
			controlplane.WithMLRegistry(mlRegistry),
		}, nostrEventRepo)
		if assistantOrchestrator != nil {
			reactorOpts = append(reactorOpts, controlplane.WithAssistantOrchestrator(assistantOrchestrator))
		}
		if dnsOperator != nil {
			reactorOpts = append(reactorOpts, controlplane.WithDNSOperator(dnsOperator))
		}
		reactorOpts = append(reactorOpts, controlplane.WithWorkerRepository(workerRepo), controlplane.WithWorkerCleanupOrchestrator(workerCleanupOrchestrator))
		// Phase 3 W1: wire worker read model publisher for direct publication
		// from mutation sites (bahia-irsry.11.14).
		workerReadModelPublisher := controlplane.NewWorkerReadModelPublisher(
			controlPlanePool, controlPlaneSigner, workerReadModelSvc, logger)
		reactorOpts = append(reactorOpts,
			controlplane.WithWorkerReadModelPublisher(workerReadModelPublisher))
		setupWorkerReadModelEventSubscriptions(publisher, workerReadModelPublisher, registry, mlRegistry, logger)
		reactorOpts = appendPackageControlPlaneOptions(reactorOpts, packageRegistrySvc, packageProjection)
		if llmRegistry != nil {
			reactorOpts = append(reactorOpts, controlplane.WithLLMRegistry(llmRegistry))
		}
		// --- D70 worker intent dual dispatch (independent of policy setup) ---
		reactorOpts = append(reactorOpts, controlplane.WithIntentProcessor(intentProcessor))
		if policyRepo != nil {
			reactorOpts = append(reactorOpts, controlplane.WithPolicyService(policySvc))
			if policyPublisher != nil {
				reactorOpts = append(reactorOpts, controlplane.WithPolicyStatePublisher(policyPublisher))
			}
		}
		// Phase 3 L1: wire LLM route publisher and ContextVM handlers.
		if llmRoutePublisher != nil {
			reactorOpts = append(reactorOpts, controlplane.WithLLMRouteStatePublisher(llmRoutePublisher))
		}
		reactor := controlplane.NewReactor(reactorConfig, registry, controlPlanePool, controlPlaneSigner, logger, reactorOpts...)
		if enabledDomains["tool"] {
			intentProcessor.RegisterHandler("tool", controlplane.NewToolIntentHandler(reactor))
		}
		if enabledDomains["worker"] && workerRepo != nil {
			intentProcessor.RegisterHandler("worker", controlplane.NewWorkerIntentHandler(reactor))
		}
		// --- end D70 worker intent registration ---
		reactor.RegisterMutationContextVMHandlers(encryptedRequestTransport, controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys))
		reactor.RegisterPackageContextVMHandlers(encryptedRequestTransport, controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys), intentProcessor)
		reactor.RegisterToolApprovalContextVMHandlers(encryptedRequestTransport, controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys))
		controlplane.RegisterLLMContextVMHandlers(encryptedRequestTransport, controlplane.NewFleetOperatorGate(cfg.Nostr.AuthorizedPubkeys), llmRegistry, intentProcessor, llmRoutePublisher)
		bgManager.RegisterWithOptions(&controlplaneRunner{reactor: reactor})
		logger.Info("nostr control plane reactor registered", zap.Strings("relays", controlPlaneRelays))
	}

	var legacyAgentReconciler *soulfactory.LegacyAgentReconciler
	if soulFactoryRuntime != nil && deploymentUnitRepo != nil {
		legacyAgentReconciler, err = soulfactory.NewLegacyAgentReconciler(
			soulFactoryRuntime.integration,
			registry,
			deploymentUnitRepo,
			soulFactoryRuntime.reactor,
			soulFactoryRuntime.reactor,
			slog.Default(),
		)
		if err != nil {
			return nil, fmt.Errorf("configure legacy Soul reconciliation: %w", err)
		}
	}

	var nip98Validator *auth.NIP98Validator
	var nip05Resolver *auth.NIP05Resolver
	if cfg.Auth.Enabled {
		if dbAvailable {
			nip98Validator = auth.NewNIP98Validator(auth.DefaultNIP98Config(), auth.NewPGNIP98ReplayStore(pool))
		} else {
			logger.Error("NIP-98 authentication unavailable without durable replay storage")
		}
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
			Virtualization:            virtualizationRepo,
			Config:                    cfg,
			AuthMiddleware:            authMiddleware,
			Builds:                    buildRepo,
			Runs:                      runRepo,
			Services:                  serviceRepo,
			Environments:              envRepo,
			EnvStates:                 stateRepo,
			InstanceOperator:          managedInstanceSupervisor,
			RuntimeResolver:           runtimeResolver,
			Payments:                  paymentSvc,
			SBOMs:                     sbomRepo,
			SBOMImporter:              sbomOrchestrator,
			Artifacts:                 artifactRepo,
			Adoption:                  adoptionSvc,
			RuntimeLifecycle:          runtimeLifecycleSvc,
			LegacyAgentReconciliation: legacyAgentReconciler,
			Notifications:             notifRepo,
			Dispatcher:                notifDispatcher,
			MCP:                       mcpHandler,
			Blossom:                   blossomClient,
			OCI:                       ociHandler,
			RBAC:                      tenantRBAC,
			ConfigFabric:              configFabricSvc,

			HealthProvider: healthProvider,
		}, cfg.Auth)

	httpServer := &http.Server{
		Addr:         cfg.ServerAddress(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	pgOutboxRepo, _ := pgNostrEventRepo.(repository.NostrEventOutboxRepository)
	nostrTransportMetrics := newNostrTransportMetricsRunner(
		telemetryProvider.GetMetrics(), localOutbox, pgOutboxRepo, 15*time.Second, logger,
		controlPlanePool, contextVMRequestPool, contextVMResponsePool, relayPool, fipsRelayPool,
	)
	if pool != nil {
		nostrTransportMetrics.setStorageSource(repository.NewPgNostrEventArchiveRepository(pool))
	}
	bgManager.RegisterWithOptions(nostrTransportMetrics, RunnerRequired(false))

	application := &App{
		Config:                    cfg,
		Logger:                    logger,
		DB:                        pool,
		Registry:                  registry,
		MLRegistry:                mlRegistry,
		LLMRegistry:               llmRegistry,
		HTTPServer:                httpServer,
		Publisher:                 publisher,
		Coordinator:               coord,
		Reconciler:                rec,
		ManagedInstanceSupervisor: managedInstanceSupervisor,
		NostrPub:                  nostrPub,
		Telemetry:                 telemetryProvider,
		Background:                bgManager,
		toolCoordinator:           toolCoordinator,
		relayPools:                []*nostrAdapter.RelayPool{controlPlanePool, contextVMRequestPool, contextVMResponsePool, relayPolicyHydrationPool, relayPool, fipsRelayPool},
		dnsBackendClosers:         dnsBackendClosers,
		TrustSet:                  trustSet,
		IntentProcessor:           intentProcessor,
		IntentReadiness:           intentReadiness,
		IntentSubscriber:          intentSubscriber,
		IntentAuthorsSyncer:       intentAuthorsSyncer,
		Health:                    healthProvider,
		RelayFirstRegistry:        relayFirstRegistry,
		SoulFactory:               soulFactoryReactorFromRuntime(soulFactoryRuntime),
		soulFactoryCloser:         soulFactoryCloserFromRuntime(soulFactoryRuntime),
		hiveCIInitiator:           hiveCIInitiator,
		localEventStore:           localEventStore,
		localOutbox:               localOutbox,
	}
	soulFactoryRuntimeReleased = true
	localNostrReleased = true
	return application, nil
}

// ReloadConfig applies the one security-sensitive scalar that can be safely
// swapped in place. Any other delta is deliberately left to the full
// candidate-application replacement path in cmd/server.
func (a *App) ReloadConfig(candidate *config.Config) (bool, error) {
	if a == nil || candidate == nil || a.Config == nil {
		return false, fmt.Errorf("application and candidate config are required")
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	currentComparable := *a.Config
	candidateComparable := *candidate
	currentRef := currentComparable.HiveCI.Initiator.MirrorReadCredentialRef
	candidateRef := candidateComparable.HiveCI.Initiator.MirrorReadCredentialRef
	candidateComparable.HiveCI.Initiator.MirrorReadCredentialRef = currentRef
	if !reflect.DeepEqual(currentComparable, candidateComparable) {
		return false, nil
	}
	if strings.TrimSpace(currentRef) == strings.TrimSpace(candidateRef) {
		return true, nil
	}
	if a.hiveCIInitiator == nil {
		return false, fmt.Errorf("HiveCI mirror credential cannot be reloaded while the initiator is disabled")
	}
	if err := a.hiveCIInitiator.ReloadMirrorReadCredentialRef(candidateRef); err != nil {
		return false, err
	}
	a.Config = candidate
	a.Logger.Info("HiveCI mirror-read credential reference reloaded")
	return true, nil
}

func soulFactoryReactorFromRuntime(runtime *soulFactoryRuntime) *soulfactory.Reactor {
	if runtime == nil {
		return nil
	}
	return runtime.reactor
}

func soulFactoryCloserFromRuntime(runtime *soulFactoryRuntime) func() error {
	if runtime == nil {
		return nil
	}
	return runtime.close
}

func defaultSupervisionPolicy(observeOnly bool) domain.RecoveryPolicy {
	return domain.RecoveryPolicy{Enabled: true, ObserveOnly: observeOnly, RestartBudget: domain.RestartBudget{MaxAttempts: 3, Window: time.Hour}, BackoffBase: time.Minute, BackoffCap: 10 * time.Minute, AlertPolicy: domain.RecoveryAlertPolicy{ImmediateSeverities: []domain.AlertSeverity{domain.AlertSeverityError, domain.AlertSeverityCritical}, WarningMinInterval: 15 * time.Minute}}
}

func configuredSupervisionSpecs(cfg config.SupervisionConfig, logger *zap.Logger) ([]service.SupervisionSpec, error) {
	result := make([]service.SupervisionSpec, 0, len(cfg.Instances))
	for i, item := range cfg.Instances {
		serviceID, err := uuid.Parse(item.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("parse supervision instance %d service: %w", i, err)
		}
		environmentID, err := uuid.Parse(item.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("parse supervision instance %d environment: %w", i, err)
		}
		unitID := uuid.Nil
		if strings.TrimSpace(item.DeploymentUnitID) != "" {
			unitID, err = uuid.Parse(item.DeploymentUnitID)
			if err != nil {
				return nil, fmt.Errorf("parse supervision instance %d deployment unit: %w", i, err)
			}
		}
		supervisor := domain.InstanceSupervisorType(item.SupervisorType)
		var observer runtime.HealthObserver
		var controller runtime.ManagedInstanceController
		switch supervisor {
		case domain.InstanceSupervisorDocker:
			adapter := runtime.NewDockerObserver(item.DockerHost, logger)
			observer, controller = adapter, adapter
		case domain.InstanceSupervisorCompose:
			adapter := runtime.NewComposeRuntimeWithDockerHost(item.ComposeDir, item.DockerHost, logger)
			observer, controller = adapter, adapter
		case domain.InstanceSupervisorSystemd, domain.InstanceSupervisorUserSystemd:
			adapter, adapterErr := runtime.NewSystemdObserver(supervisor)
			if adapterErr != nil {
				return nil, adapterErr
			}
			observer, controller = adapter, adapter
		default:
			return nil, fmt.Errorf("unsupported supervision instance %d supervisor %q", i, supervisor)
		}
		policy := defaultSupervisionPolicy(cfg.ObserveOnly)
		policy.RestartBudget = domain.RestartBudget{MaxAttempts: item.RestartMaxAttempts, Window: item.RestartWindow}
		policy.BackoffBase, policy.BackoffCap = item.BackoffBase, item.BackoffCap
		policy.AlertPolicy.WarningMinInterval = item.WarningMinInterval
		spec := service.SupervisionSpec{Key: domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: unitID, RuntimeTargetName: item.RuntimeTargetName}, Host: item.Host, SupervisorType: supervisor, RecoveryPolicy: policy, DesiredRunning: item.DesiredRunning, Observer: observer, Controller: controller, MemoryThresholdRatio: cfg.MemoryThreshold}
		if strings.TrimSpace(item.ProbeURL) != "" {
			probe := runtime.HTTPProbeConfig{URL: item.ProbeURL, Timeout: item.ProbeTimeout, ExpectedStatusMin: http.StatusOK, ExpectedStatusMax: 299}
			spec.ProbeConfig, spec.Prober = &probe, runtime.HTTPProber{}
		}
		result = append(result, spec)
	}
	return result, nil
}

func connectOptionalDatabase(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*pgxpool.Pool, bool) {
	pool, err := dbConnect(ctx, cfg.DB, logger)
	if err != nil {
		logger.Warn("postgres cache unavailable; continuing with relay-first reduced tier", zap.Error(cfg.DB.RedactError(err)))
		return nil, false
	}
	if err := dbMigrate(ctx, pool, logger); err != nil {
		pool.Close()
		logger.Warn("postgres cache migration failed; continuing with relay-first reduced tier", zap.Error(err))
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
	if existing, ok := r.store[key]; ok && !projectionMetaVersion(meta).Supersedes(projectionMetaVersion(*existing)) {
		return nil
	}
	r.store[key] = &meta
	return nil
}

// projectionMetaVersion orders metadata like the cache orders projections:
// later timestamp, then lowest source event id (C-13). Keeping the higher id on
// a same-second tie would let a third version between the two win.
func projectionMetaVersion(meta repository.RelayProjectionMeta) nostrutil.Version {
	return nostrutil.Version{CreatedAt: nostr.Timestamp(meta.UpdatedAt.Unix()), ID: meta.SourceEventID}
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
	statusProjector *service.BahiaStatusProjector
	catalogVersion  string
	logger          *zap.Logger
}

const bootstrapStatusPublishTimeout = 5 * time.Second

func (r *bootstrapperRunner) Name() string { return "relay-bootstrapper" }
func (r *bootstrapperRunner) Run(ctx context.Context) error {
	if r.bootstrapper == nil {
		return fmt.Errorf("relay bootstrapper is not configured")
	}

	// Publish identity at startup.
	if r.statusProjector != nil {
		err := runBootstrapStatusPublication(ctx, bootstrapStatusPublishTimeout, func(publishCtx context.Context) error {
			return r.statusProjector.PublishIdentity(publishCtx, service.BahiaIdentityPayload{
				Version:        "1.0.0",
				CatalogVersion: r.catalogVersion,
				Mode:           "relay-first",
				StartedAt:      time.Now().Unix(),
			})
		})
		if err != nil && r.logger != nil {
			r.logger.Warn("Bahia identity publication did not complete before bootstrap; continuing", zap.Error(err))
		}
	}

	err := r.bootstrapper.Run(ctx)

	// Publish checkpoint and readiness after bootstrap completes.
	if r.statusProjector != nil {
		progress := r.bootstrapper.Progress()
		if pubErr := runBootstrapStatusPublication(ctx, bootstrapStatusPublishTimeout, func(publishCtx context.Context) error {
			return r.statusProjector.PublishCheckpoint(publishCtx, service.ReplayCheckpointPayload{
				CatalogVersion: r.catalogVersion,
				Phase:          string(progress.Phase),
			})
		}); pubErr != nil {
			err = errors.Join(err, pubErr)
		}
		if pubErr := runBootstrapStatusPublication(ctx, bootstrapStatusPublishTimeout, func(publishCtx context.Context) error {
			return r.statusProjector.PublishReadiness(publishCtx, service.ReadinessStatusPayload{
				Phase: string(progress.Phase),
				Ready: r.bootstrapper.Ready(),
			})
		}); pubErr != nil {
			err = errors.Join(err, pubErr)
		}
	}

	return err
}

func runBootstrapStatusPublication(ctx context.Context, timeout time.Duration, publish func(context.Context) error) error {
	if publish == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = bootstrapStatusPublishTimeout
	}
	publishCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- publish(publishCtx)
	}()

	select {
	case err := <-result:
		return err
	case <-publishCtx.Done():
		return publishCtx.Err()
	}
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
	cleanupStatePublisher *controlplane.WorkerCleanupStatePublisher,
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
		if workerRepo != nil {
			if err := workerRepo.Upsert(ctx, &observed.Worker); err != nil {
				logger.Warn("persist worker telemetry observation failed", zap.String("worker_pubkey", observed.Worker.PubKey), zap.Error(err))
			}
		}
		if statePublisher != nil {
			if err := statePublisher.Publish(ctx, &observed.Worker); err != nil {
				logger.Warn("publish worker state after telemetry observation failed", zap.String("worker_pubkey", observed.Worker.PubKey), zap.Error(err))
			}
		}
	})
	publishCleanupState := func(ctx context.Context, e events.Event) {
		cleanup, ok := e.Data.(events.WorkerCleanupEvent)
		if !ok || cleanupStatePublisher == nil {
			if !ok {
				logger.Warn("worker cleanup event carried unsupported payload", zap.String("event_type", string(e.Type)))
			}
			return
		}
		if err := cleanupStatePublisher.Publish(ctx, cleanup); err != nil {
			logger.Warn("publish worker cleanup state failed", zap.String("worker_pubkey", cleanup.WorkerPubKey), zap.String("status", cleanup.Status), zap.Error(err))
		}
	}
	publisher.Subscribe(events.EventWorkerCleanupRequested, publishCleanupState)
	publisher.Subscribe(events.EventWorkerCleanupCompleted, publishCleanupState)
	publisher.Subscribe(events.EventWorkerCleanupFailed, publishCleanupState)

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

// setupWorkerReadModelEventSubscriptions wires event-bus subscriptions so that
// deployment-run and ML-run lifecycle events trigger an immediate worker
// read-model republish (assignment, drain, eligibility). This replaces the
// projector's reactive handleEvent cases removed in Phase 3 W1
// (bahia-irsry.11.14).
func setupWorkerReadModelEventSubscriptions(
	pub events.Publisher,
	workerReadModelPublisher *controlplane.WorkerReadModelPublisher,
	registry *service.RegistryService,
	mlRegistry *service.MLRegistryService,
	logger *zap.Logger,
) {
	if pub == nil || workerReadModelPublisher == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	// Deployment run events: look up the run to extract the worker pubkey,
	// then republish assignment/drain/eligibility for that worker.
	publishForDeploymentRun := func(ctx context.Context, e events.Event) {
		runID := e.EntityID
		if res, ok := e.Data.(events.ResourceData); ok && res.RunID != "" {
			runID = res.RunID
		}
		id, err := uuid.Parse(runID)
		if err != nil {
			return
		}
		run, err := registry.GetDeploymentRun(ctx, id)
		if err != nil || run == nil || run.WorkerPubkey == "" {
			if err != nil {
				logger.Warn("lookup deployment run for worker read model refresh failed",
					zap.String("run_id", runID), zap.Error(err))
			}
			return
		}
		workerReadModelPublisher.PublishForWorker(ctx, run.WorkerPubkey)
	}
	pub.Subscribe(events.EventDeploymentRunCreated, publishForDeploymentRun)
	pub.Subscribe(events.EventDeploymentRunStatusChanged, publishForDeploymentRun)
	pub.Subscribe(events.EventDeploymentRunCompleted, publishForDeploymentRun)

	// ML deployment run events: same pattern, using the ML registry.
	if mlRegistry != nil {
		pub.Subscribe(service.EventMLRunChanged, func(ctx context.Context, e events.Event) {
			runID := e.EntityID
			if m, ok := e.Data.(map[string]any); ok {
				if rid, ok := m["run_id"].(string); ok && rid != "" {
					runID = rid
				}
			}
			id, err := uuid.Parse(runID)
			if err != nil {
				return
			}
			run, err := mlRegistry.GetMLDeploymentRun(ctx, id)
			if err != nil || run == nil || run.WorkerPubkey == "" {
				if err != nil {
					logger.Warn("lookup ML deployment run for worker read model refresh failed",
						zap.String("run_id", runID), zap.Error(err))
				}
				return
			}
			workerReadModelPublisher.PublishForWorker(ctx, run.WorkerPubkey)
		})
	}
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
	status, err := c.client.AwaitJobStatusFromWorker(ctx, jobEventID, expectedWorkerPubkey, loomCallbacks...)
	return cleanupJobStatusFromLoom(status), err
}

func cleanupJobStatusFromLoom(status *loom.JobStatus) *service.CleanupJobStatus {
	if status == nil {
		return nil
	}
	return &service.CleanupJobStatus{JobID: status.JobID, Status: status.Status, Success: status.Success, ExitCode: status.ExitCode, Duration: status.Duration, WorkerPubkey: status.WorkerPubkey, StdoutURL: status.StdoutURL, StderrURL: status.StderrURL, ChangeToken: status.ChangeToken, Error: status.Error, LogOutput: status.LogOutput}
}

func startBackgroundRunners(ctx context.Context, manager *BackgroundManager, fatalErrCh chan<- error) {
	if manager == nil {
		return
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()

	for _, reg := range manager.runners {
		manager.wg.Add(1)
		go func(reg backgroundRunnerRegistration) {
			runner := reg.runner
			defer manager.wg.Done()
			manager.logger.Info("background runner starting", zap.String("name", runner.Name()))
			manager.markRunnerStarted(runner.Name())

			err := runner.Run(ctx)
			manager.markRunnerStopped(runner.Name(), err, ctx.Err() != nil)
			if err != nil && ctx.Err() == nil {
				if fatalErrCh != nil && errors.Is(err, errBackgroundRestartRequired) {
					select {
					case fatalErrCh <- err:
					default:
					}
				}
				manager.logger.Error("background runner exited with error", zap.String("name", runner.Name()), zap.Error(err))
			} else {
				manager.logger.Info("background runner stopped", zap.String("name", runner.Name()))
			}
		}(reg)
	}
}

func (a *App) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.RunContext(ctx)
}

// RunContext runs the application until ctx is cancelled. Command supervisors
// use it to apply a validated mounted-config reload without recreating the
// container; Run retains the standalone signal-driven behavior.
func (a *App) RunContext(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		a.Logger.Info("HTTP server starting", zap.String("addr", a.HTTPServer.Addr))
		if err := a.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	runnerErrCh := make(chan error, 1)

	// Start allowed background runners after HTTP is accepting connections.
	startBackgroundRunners(ctx, a.Background, runnerErrCh)

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case err := <-runnerErrCh:
		return err
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

	if a.soulFactoryCloser != nil {
		if err := a.soulFactoryCloser(); err != nil {
			a.Logger.Warn("SoulFactory Signet client close failed", zap.Error(err))
		}
	}

	// Shutdown telemetry.
	if a.Telemetry != nil {
		_ = a.Telemetry.Shutdown(shutdownCtx)
	}

	// Close DNS backends that own remote transports (e.g. the dnsmasq agent's
	// ContextVM relay pool) so reload-built Apps do not leak goroutines/websockets.
	for _, closer := range a.dnsBackendClosers {
		if err := closer.Close(); err != nil {
			a.Logger.Warn("DNS backend close failed", zap.Error(err))
		}
	}

	// Close Nostr relay connections.
	closeRelayPools(a.relayPools...)
	if a.localEventStore != nil {
		if err := a.localEventStore.Close(); err != nil {
			a.Logger.Warn("local Nostr event store close failed", zap.Error(err))
		}
	}
	if a.localOutbox != nil {
		if err := a.localOutbox.Close(); err != nil {
			a.Logger.Warn("local Nostr publish outbox close failed", zap.Error(err))
		}
	}

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

type dnsPolicyRepositoryProjectionSource struct {
	repo repository.DNSPolicyRepository
}

func (s dnsPolicyRepositoryProjectionSource) ListEnabledDNSPolicies(ctx context.Context) ([]domain.DNSPolicy, error) {
	return s.repo.ListEnabled(ctx)
}

type configDNSBackendProjectionSource struct {
	backends map[string]config.DNSBackendConfig
	zones    []domain.DNSZone
	resolver dnsAdapter.Resolver
}

type localDNSBackendProjectionSource struct {
	repo   repository.DNSBackendRepository
	logger *zap.Logger
}

func (s localDNSBackendProjectionSource) ListDNSBackendStates(ctx context.Context) []domain.DNSBackendState {
	states, err := s.repo.List(ctx)
	if err != nil {
		s.logger.Warn("loading durable DNS backend states failed", zap.Error(err))
		return nil
	}
	return states
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
	zonesMu    sync.RWMutex
	zones      map[string]struct{}
	backends   map[string]struct{}
}

func newDNSControlPlaneOperator(reconciler *reconcile.DNSReconciler, zones []domain.DNSZone, backendRefs []string, persistence controlplane.DNSPersistenceOperator, policies repository.DNSPolicyRepository) controlplane.DNSControlPlaneOperator {
	zoneSet := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		zoneSet[strings.TrimSpace(zone.Name)] = struct{}{}
	}
	backendSet := make(map[string]struct{}, len(backendRefs))
	for _, ref := range backendRefs {
		backendSet[strings.TrimSpace(ref)] = struct{}{}
	}
	operator := &dnsControlPlaneOperator{reconciler: reconciler, zones: zoneSet, backends: backendSet}
	if persistence != nil {
		persistent := &dnsPersistentControlPlaneOperator{dnsControlPlaneOperator: operator, persistence: persistence, policies: policies}
		// Advertise override retirement only when the underlying persistence can
		// actually perform it. Returning a type that always implements the
		// capability would make the handler's check meaningless and turn an
		// unsupported backend into a runtime error instead of a clean
		// "unsupported" result.
		if retirer, ok := persistence.(controlplane.DNSOverrideRetirementOperator); ok {
			return &dnsRetiringControlPlaneOperator{dnsPersistentControlPlaneOperator: persistent, retirer: retirer}
		}
		return persistent
	}
	return operator
}

// dnsRetiringControlPlaneOperator adds the optional override-retirement
// capability to a persistent DNS operator whose backend supports it.
type dnsRetiringControlPlaneOperator struct {
	*dnsPersistentControlPlaneOperator
	retirer controlplane.DNSOverrideRetirementOperator
}

func (o *dnsRetiringControlPlaneOperator) GetOverride(ctx context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	return o.retirer.GetOverride(ctx, id)
}

func (o *dnsRetiringControlPlaneOperator) ExpireOverride(ctx context.Context, id uuid.UUID, at time.Time, reason string) error {
	return o.retirer.ExpireOverride(ctx, id, at, reason)
}

func (o *dnsControlPlaneOperator) ReconcileAll(ctx context.Context) error {
	if o.reconciler == nil {
		return fmt.Errorf("DNS reconciler is not configured")
	}
	return o.reconciler.ReconcileOnce(ctx)
}

func (o *dnsControlPlaneOperator) RetireZone(ctx context.Context, zone domain.DNSZone) error {
	if o.reconciler == nil {
		return fmt.Errorf("DNS reconciler is not configured")
	}
	return o.reconciler.RetireZone(ctx, zone)
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
	o.zonesMu.RLock()
	defer o.zonesMu.RUnlock()
	_, ok := o.zones[strings.TrimSpace(zoneName)]
	return ok
}

func (o *dnsControlPlaneOperator) SetZoneActive(zoneName string, active bool) {
	o.zonesMu.Lock()
	defer o.zonesMu.Unlock()
	if active {
		o.zones[zoneName] = struct{}{}
	} else {
		delete(o.zones, zoneName)
	}
}

func (o *dnsControlPlaneOperator) HasBackend(ref string) bool {
	_, ok := o.backends[strings.TrimSpace(ref)]
	return ok
}

type dnsPersistentControlPlaneOperator struct {
	*dnsControlPlaneOperator
	persistence controlplane.DNSPersistenceOperator
	policies    repository.DNSPolicyRepository
}

func (o *dnsPersistentControlPlaneOperator) DNSPolicyRepository() repository.DNSPolicyRepository {
	return o.policies
}

func (o *dnsPersistentControlPlaneOperator) CreateZone(ctx context.Context, zone domain.DNSZone) error {
	if err := o.persistence.CreateZone(ctx, zone); err != nil {
		return err
	}
	o.zonesMu.Lock()
	defer o.zonesMu.Unlock()
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

func (a dnsRepositoryPersistenceAdapter) GetOverride(ctx context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	if a.overrides == nil {
		return nil, fmt.Errorf("DNS record override repository is not configured")
	}
	return a.overrides.Get(ctx, id)
}

func (a dnsRepositoryPersistenceAdapter) ExpireOverride(ctx context.Context, id uuid.UUID, at time.Time, _ string) error {
	if a.overrides == nil {
		return fmt.Errorf("DNS record override repository is not configured")
	}
	return a.overrides.Expire(ctx, id, at)
}

func buildPublicRoutePlanner(ctx context.Context, cfg config.EdgeRoutingConfig, internalCfg config.InternalRoutingConfig, secretRepo repository.SecretRepository, encryptor *secretsAdapter.Encryptor, logger *zap.Logger) (*service.PublicRoutePlanner, *routingAdapter.NginxBackend, error) {
	resolver := secretsAdapter.NewResolver(secretRepo, encryptor)
	tokenPayload, err := resolver.ResolveSecret(ctx, cfg.APITokenRef)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve Cloudflare API token reference: %w", err)
	}
	token := strings.TrimSpace(tokenPayload)
	if strings.HasPrefix(token, "{") {
		var credentials map[string]any
		if err := json.Unmarshal([]byte(token), &credentials); err != nil {
			return nil, nil, fmt.Errorf("decode Cloudflare credential bundle")
		}
		for _, key := range []string{"api_token", "token", "APIToken"} {
			if value, ok := credentials[key].(string); ok && strings.TrimSpace(value) != "" {
				token = strings.TrimSpace(value)
				break
			}
		}
	}
	if token == "" || strings.HasPrefix(token, "{") {
		return nil, nil, fmt.Errorf("cloudflare credential secret does not contain an API token")
	}
	zoneIDs := make(map[string]string, len(cfg.Zones))
	zones := make([]service.PublicRouteZone, 0, len(cfg.Zones))
	for _, z := range cfg.Zones {
		allowed := make([]uuid.UUID, 0, len(z.AllowedOrgIDs))
		for _, raw := range z.AllowedOrgIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("parse allowed organization ID: %w", err)
			}
			allowed = append(allowed, id)
		}
		zoneIDs[z.Name] = z.ZoneID
		zones = append(zones, service.PublicRouteZone{Name: z.Name, BackendRef: cfg.BackendRef, AllowedOrgIDs: allowed, Protected: z.Protected, TTL: z.TTL})
	}
	origins := make([]service.PublicRouteOrigin, 0, len(cfg.Origins))
	for _, o := range cfg.Origins {
		id, err := uuid.Parse(o.DeploymentUnitID)
		if err != nil {
			return nil, nil, err
		}
		origins = append(origins, service.PublicRouteOrigin{DeploymentUnitID: id, Host: o.Host, AllowedPorts: append([]int(nil), o.AllowedPorts...)})
	}
	cloudflare, err := routingAdapter.NewCloudflareBackend(routingAdapter.CloudflareConfig{APIBaseURL: cfg.APIBaseURL, APIToken: token, AccountID: cfg.AccountID, TunnelID: cfg.TunnelID, ZoneIDs: zoneIDs, VerifyTimeout: cfg.VerifyTimeout, VerifyResolverAddr: cfg.VerifyResolver}, nil)
	if err != nil {
		return nil, nil, err
	}
	sanitized, _ := json.Marshal(struct {
		Provider, APIBaseURL, AccountID, TunnelID, BackendRef string
		Zones                                                 []config.EdgeRoutingZoneConfig
		Origins                                               []config.EdgeRoutingOriginConfig
	}{cfg.Provider, cfg.APIBaseURL, cfg.AccountID, cfg.TunnelID, cfg.BackendRef, cfg.Zones, cfg.Origins})
	plannerConfig := service.PublicRoutePlannerConfig{
		Provider: cfg.Provider, TunnelRef: cfg.TunnelID, DNSTarget: cfg.TunnelID + ".cfargotunnel.com",
		Zones: zones, Origins: origins, ConfigHash: domain.PublicRouteProviderConfigHash(string(sanitized)),
	}
	var backend routingAdapter.Backend = cloudflare
	var nginx *routingAdapter.NginxBackend
	if internalCfg.Enabled {
		internalHash := internalRoutingConfigHash(internalCfg)
		nginx, err = routingAdapter.NewNginxBackend(routingAdapter.NginxConfig{
			IncludeDir: internalCfg.IncludeDir, FilePrefix: internalCfg.FilePrefix,
			TestCommand: internalCfg.TestCommand, ReloadCommand: internalCfg.ReloadCommand, CommandEnv: internalCfg.CommandEnv,
			CertFile: internalCfg.CertFile, KeyFile: internalCfg.KeyFile, ConfigHash: internalHash, Logger: logger,
		})
		if err != nil {
			return nil, nil, err
		}
		backend, err = routingAdapter.NewCompositeBackend(cloudflare, nginx)
		if err != nil {
			return nil, nil, err
		}
		plannerConfig.InternalHTTPS = &service.InternalHTTPSPlannerConfig{
			Provider: internalCfg.Provider, Listen: "443 ssl", CertFile: internalCfg.CertFile,
			KeyFile: internalCfg.KeyFile, ConfigHash: internalHash, Zones: append([]string(nil), internalCfg.Zones...),
		}
	}
	planner, err := service.NewPublicRoutePlanner(plannerConfig, routingAdapter.StaticResolver{cfg.BackendRef: backend})
	if err != nil {
		return nil, nil, err
	}
	return planner, nginx, nil
}

func internalRoutingConfigHash(cfg config.InternalRoutingConfig) string {
	// The hash must cover command environment values without exposing the
	// serialized bytes. Use a local alias to bypass the diagnostic redaction
	// boundary only for this immediate one-way hash input.
	type internalRoutingHashInput config.InternalRoutingConfig
	encoded, _ := json.Marshal(internalRoutingHashInput(cfg))
	return domain.PublicRouteProviderConfigHash(string(encoded))
}

// buildDNSRuntime returns the configured zones and backend resolver plus the
// closers for backends that own remote transports; the caller must close them
// on shutdown. On error, any already-created backends are closed before return.
func buildDNSRuntime(ctx context.Context, cfg config.DNSConfig, controlPlaneRelays []string, signer nostr.Signer, senderPubkey string, agentHealthReader *dnsAdapter.AgentHealthReader, deferredPublisher *dnsAdapter.DeferredZoneSyncPublisher, logger *zap.Logger) ([]domain.DNSZone, *dnsAdapter.StaticResolver, []io.Closer, error) {
	var closers []io.Closer
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		for _, closer := range closers {
			if err := closer.Close(); err != nil && logger != nil {
				logger.Warn("DNS backend close failed during failed startup", zap.Error(err))
			}
		}
	}()
	zones := make([]domain.DNSZone, 0, len(cfg.Zones))
	for _, zoneConfig := range cfg.Zones {
		ttl := zoneConfig.TTL
		if ttl <= 0 {
			ttl = cfg.DefaultTTL
		}
		zone := domain.DNSZone{
			Name:                    strings.TrimSpace(zoneConfig.Name),
			Visibility:              domain.ZoneVisibility(strings.TrimSpace(zoneConfig.Visibility)),
			BackendRef:              strings.TrimSpace(zoneConfig.Backend),
			TTL:                     ttl,
			Authoritative:           zoneConfig.Authoritative,
			AllowEmptyAuthoritative: zoneConfig.AllowEmptyAuthoritative,
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			return nil, nil, nil, fmt.Errorf("configuring DNS zone %q: %w", zoneConfig.Name, err)
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
			return nil, nil, nil, fmt.Errorf("configuring DNS filesystem backend %q: filesystem backends are rejected during config validation because no operational activator is wired", ref)
		case string(domain.DNSBackendTypeCoreDNS):
			backend, err := dnsAdapter.NewCoreDNSBackend(dnsAdapter.CoreDNSConfig{EtcdEndpoints: backendConfig.EtcdEndpoints, EtcdPrefix: backendConfig.EtcdPrefix, DialTimeout: backendConfig.EtcdDialTimeout})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("configuring DNS CoreDNS backend %q: %w", ref, err)
			}
			if err := backend.Health(ctx); err != nil {
				return nil, nil, nil, fmt.Errorf("checking DNS CoreDNS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypePowerDNS):
			backend, err := dnsAdapter.NewPowerDNSBackend(dnsAdapter.PowerDNSConfig{APIURL: backendConfig.PowerDNSAPIURL, APIKey: backendConfig.PowerDNSAPIKey, ServerID: backendConfig.PowerDNSServerID, AllowInsecureHTTP: backendConfig.PowerDNSAllowInsecureHTTP})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("configuring DNS PowerDNS backend %q: %w", ref, err)
			}
			if err := backend.Health(ctx); err != nil {
				return nil, nil, nil, fmt.Errorf("checking DNS PowerDNS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeDNSMasq):
			backend := dnsAdapter.NewDnsmasqBackend(dnsAdapter.DnsmasqConfig{ConfigDir: backendConfig.DnsmasqConfigDir, ReloadCommand: backendConfig.DnsmasqReloadCommand, FilePrefix: backendConfig.DnsmasqFilePrefix})
			if err := backend.Health(ctx); err != nil {
				return nil, nil, nil, fmt.Errorf("checking DNS dnsmasq backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeDnsmasqAgent):
			relays := backendConfig.AgentRelays
			if len(relays) == 0 {
				relays = controlPlaneRelays
			}
			rpcBackend, err := dnsAdapter.NewRelayDnsmasqAgentBackend(dnsAdapter.DnsmasqAgentConfig{
				Relays:        relays,
				Signer:        signer,
				SenderPubkey:  senderPubkey,
				AgentPubkey:   backendConfig.AgentPubkey,
				Encrypted:     backendConfig.AgentEncrypted,
				ResultTimeout: backendConfig.AgentTimeout,
				ResultRetries: backendConfig.AgentRetries,
			}, logger)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("configuring DNS dnsmasq agent backend %q: %w", ref, err)
			}
			// Phase 3 D1 (C-34): wrap with capability-aware backend. When the
			// agent advertises "zone-subscribe" via NIP-38, use event publish;
			// otherwise fall back to ContextVM RPC for old agents.
			var backend dnsAdapter.Backend
			if agentHealthReader != nil && deferredPublisher != nil {
				eventBackend := dnsAdapter.NewEventPublishDNSBackend(deferredPublisher, logger)
				capBackend := dnsAdapter.NewCapabilityAwareDNSBackend(rpcBackend, eventBackend, agentHealthReader, backendConfig.AgentPubkey, logger)
				closers = append(closers, capBackend)
				backend = capBackend
			} else {
				closers = append(closers, rpcBackend)
				backend = rpcBackend
			}
			// The agent is a remote, relay-backed dependency. Do not make Bahia's
			// process startup depend on a synchronous ContextVM round trip: the DNS
			// reconciler performs the same health check continuously and surfaces
			// failures without taking down the control plane that must repair them.
			if logger != nil {
				logger.Info("DNS dnsmasq agent startup health deferred to reconciler", zap.String("backend", ref))
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		case string(domain.DNSBackendTypeFIPS):
			backend := dnsAdapter.NewFIPSBackend(backendConfig.HostsPath, logger)
			if err := backend.Health(ctx); err != nil {
				return nil, nil, nil, fmt.Errorf("checking DNS FIPS backend %q: %w", ref, err)
			}
			registrations = append(registrations, dnsAdapter.BackendRegistration{Ref: ref, Backend: backend})
		default:
			return nil, nil, nil, fmt.Errorf("configuring DNS backend %q: unsupported type %q", ref, backendConfig.Type)
		}
	}
	resolver, err := dnsAdapter.NewStaticResolver(registrations...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configuring DNS backend resolver: %w", err)
	}
	for _, zone := range zones {
		if _, ok := resolver.Resolve(zone.BackendRef); !ok {
			return nil, nil, nil, fmt.Errorf("configuring DNS zone %q: backend %q is not registered", zone.Name, zone.BackendRef)
		}
	}
	if logger != nil {
		logger.Info("DNS runtime configured", zap.Int("zones", len(zones)), zap.Strings("backends", resolver.Refs()))
	}
	succeeded = true
	return zones, resolver, closers, nil
}

// agentHealthSubscriber is a BackgroundRunner that subscribes to NIP-38 kind
// 30315 health status events from DNS agents, feeding them to the
// AgentHealthReader so the daemon reads agent health and capabilities from
// events instead of ContextVM Health() RPCs (C-34, Phase 3 D1).
type agentHealthSubscriber struct {
	pool    *nostrAdapter.RelayPool
	pubkeys []string
	reader  *dnsAdapter.AgentHealthReader
	logger  *zap.Logger
}

func (s *agentHealthSubscriber) Name() string { return "dns-agent-health-subscriber" }

func (s *agentHealthSubscriber) Run(ctx context.Context) error {
	return subscribeAgentHealth(ctx, s.pool, s.pubkeys, s.reader, s.logger)
}

// subscribeAgentHealth subscribes to NIP-38 kind 30315 events from the given
// agent pubkeys and forwards them to the health reader.
func subscribeAgentHealth(ctx context.Context, pool *nostrAdapter.RelayPool, agentPubkeys []string, reader *dnsAdapter.AgentHealthReader, logger *zap.Logger) error {
	pubkeys := make([]nostr.PubKey, 0, len(agentPubkeys))
	for _, hex := range agentPubkeys {
		pk, err := nostr.PubKeyFromHex(hex)
		if err != nil {
			logger.Warn("invalid agent pubkey for health subscription", zap.String("pubkey", hex), zap.Error(err))
			continue
		}
		pubkeys = append(pubkeys, pk)
	}
	if len(pubkeys) == 0 {
		return nil
	}

	filter := nostr.Filter{
		Kinds:   []nostr.Kind{30315},
		Authors: pubkeys,
		Tags:    nostr.TagMap{"d": []string{"dns-agent"}},
	}
	events, err := pool.SubscribeAll(ctx, []nostr.Filter{filter})
	if err != nil {
		return fmt.Errorf("subscribe to agent health events: %w", err)
	}
	logger.Info("agent health subscription started", zap.Int("agents", len(pubkeys)))
	for ev := range events {
		if ev != nil {
			reader.HandleEvent(ctx, *ev)
		}
	}
	return nil
}

func shouldRegisterHiveCIRunners(cfg config.HiveCIConfig) bool {
	return cfg.Enabled
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
	return cfg.ContextVMRelayPolicyRelays()
}

func contextVMRelayURLs(cfg config.NostrConfig) []string {
	var relays []string
	if cfg.Sidecar.Enabled {
		if cfg.Sidecar.BackendURL != "" {
			relays = appendUniqueRelay(relays, cfg.Sidecar.BackendURL)
		} else {
			relays = appendUniqueRelay(relays, cfg.Sidecar.PublicURL)
		}
	}
	for _, relay := range cfg.ContextVMRelayPolicyRelays() {
		relays = appendUniqueRelay(relays, relay)
	}
	return relays
}

func relayPolicyHydrationRelayURLs(cfg config.NostrConfig) []string {
	var relays []string
	if cfg.Sidecar.Enabled {
		relays = appendUniqueRelay(relays, cfg.Sidecar.BackendURL)
		relays = appendUniqueRelay(relays, cfg.Sidecar.PublicURL)
	}
	for _, candidates := range [][]string{
		cfg.ContextVMRelays,
		cfg.BrowserRelays,
		cfg.ServiceRelays,
		cfg.Relays,
		cfg.NIP34Relays,
	} {
		for _, relay := range candidates {
			relays = appendUniqueRelay(relays, relay)
		}
	}
	return relays
}

func relayPolicyHydrationRelayURLsForState(configured []string, state controlplane.RelayPolicyState) []string {
	relays := append([]string(nil), configured...)
	for _, candidates := range [][]string{
		state.ContextVMRelays,
		state.BrowserRelays,
		state.ServiceRelays,
		state.NIP34Relays,
	} {
		for _, relay := range candidates {
			relays = appendUniqueRelay(relays, relay)
		}
	}
	return relays
}

func interopRelayURLs(cfg *config.Config, controlPlaneRelays []string) []string {
	var relays []string
	if cfg.Nostr.Sidecar.Enabled {
		// The sidecar is always the canonical Bahia service boundary. Generic
		// inbound subscribers, bootstrap replay, and service projections must
		// retain it even when no public interop relays are configured.
		for _, r := range controlPlaneRelays {
			relays = appendUniqueRelay(relays, r)
		}
	}
	if cfg.Nostr.Sidecar.Enabled && cfg.Nostr.Sidecar.MirrorExternal {
		// The sidecar is the upstream mirror boundary. Subscribe through it for
		// public interop/audit traffic instead of also connecting Bahia directly to
		// cfg.Nostr.Relays, which would create duplicate publish/subscribe loops.
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

func llmGatewayHTTPConfig(cfg config.LLMControlplaneConfig) (llmadapter.GatewayHTTPConfig, error) {
	endpoints := make(map[string]llmadapter.GatewayHTTPEndpointConfig, len(cfg.Gateways))
	for ref, ep := range cfg.Gateways {
		authToken := strings.TrimSpace(ep.AuthToken)
		if file := strings.TrimSpace(ep.AuthTokenFile); file != "" {
			raw, err := os.ReadFile(file)
			if err != nil {
				return llmadapter.GatewayHTTPConfig{}, fmt.Errorf("read llm.gateways.%s.auth_token_file %q: %w", ref, file, err)
			}
			authToken = strings.TrimSpace(string(raw))
			if authToken == "" {
				return llmadapter.GatewayHTTPConfig{}, fmt.Errorf("llm.gateways.%s.auth_token_file %q is empty", ref, file)
			}
		}
		endpoints[ref] = llmadapter.GatewayHTTPEndpointConfig{Type: ep.Type, BaseURL: ep.BaseURL, AuthToken: authToken, Timeout: ep.Timeout}
	}
	return llmadapter.GatewayHTTPConfig{Endpoints: endpoints}, nil
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

func (p *auditedNostrPublisher) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if p == nil || p.delegate == nil {
		return 0, fmt.Errorf("audited nostr publisher delegate is not configured")
	}
	published, err := p.delegate.Publish(ctx, ev)
	if err == nil && published > 0 && p.repo != nil {
		tagsJSON, marshalErr := json.Marshal(ev.Tags)
		if marshalErr != nil {
			if p.logger != nil {
				p.logger.Warn("failed to marshal audited assistant event tags", zap.String("event_id", ev.ID.Hex()), zap.Error(marshalErr))
			}
		} else if _, recordErr := p.repo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: hex.EncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC()}); recordErr != nil && p.logger != nil {
			p.logger.Warn("failed to audit assistant event", zap.String("event_id", ev.ID.Hex()), zap.Error(recordErr))
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

func configureToolApprovalMCPDeps(deps *mcp.ServerDeps, publisher controlplane.NostrEventPublisher, signer nostr.Signer, relays []string) {
	if deps == nil || publisher == nil || signer == nil || len(relays) == 0 {
		return
	}
	deps.ToolApprovalCommandPublisher = controlplane.NewToolApprovalCommandPublisher(publisher, signer)
}

// newTenantRBAC leaves tenant authorization unconfigured when no durable
// membership lookup is available. Consumers must treat a nil RBAC as a denial.
func newTenantRBAC(members auth.OrgMemberLookup) *auth.RBAC {
	if members == nil {
		return nil
	}
	return auth.NewRBAC(members)
}

func configureAuthorizationMCPDeps(deps *mcp.ServerDeps, cfg *config.Config, rbac *auth.RBAC) {
	if deps == nil {
		return
	}
	deps.RBAC = rbac
	if cfg == nil {
		return
	}
	deps.AuthorizedPubkeys = cfg.Nostr.AuthorizedPubkeys
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
			if secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey)); err == nil {
				add(secret.Public().Hex())
			}
		}
	}
	add(assistant.Pubkey)
	return out
}

func newAssistantAgentModelClient(cfg config.AssistantAgenticConfig, logger *slog.Logger) (llmadapter.AgentModelClient, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "openai_compatible":
		if strings.ToLower(strings.TrimSpace(cfg.ToolMode)) == config.AssistantAgenticToolModePrompted {
			return llmadapter.NewPromptedAgentClient(llmadapter.PromptedAgentClientConfig{
				BaseURL: cfg.BaseURL,
				Model:   cfg.Model,
				APIKey:  cfg.APIKey,
				Timeout: cfg.RequestTimeout,
			}, logger), nil
		}
		return llmadapter.NewOpenAIAgentClient(llmadapter.OpenAIAgentClientConfig{
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			APIKey:  cfg.APIKey,
			Timeout: cfg.RequestTimeout,
		}, logger), nil
	case "anthropic":
		return llmadapter.NewAnthropicAgentClient(llmadapter.AnthropicAgentClientConfig{
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			APIKey:  cfg.APIKey,
			Timeout: cfg.RequestTimeout,
		}, logger), nil
	default:
		return nil, fmt.Errorf("unsupported assistant.agentic.provider %q", cfg.Provider)
	}
}

type assistantExternalMCPRuntime struct {
	clients         map[string]*mcpclient.Client
	descriptors     []mcp.ExternalToolDescriptor
	permissionRules []service.AssistantPermissionRule
}

func loadAssistantExternalMCP(ctx context.Context, servers []config.AssistantExternalMCPServerConfig, logger *slog.Logger) (assistantExternalMCPRuntime, error) {
	bundle := assistantExternalMCPRuntime{clients: map[string]*mcpclient.Client{}}
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		client := mcpclient.NewClient(mcpclient.Config{
			Name:        server.Name,
			URL:         server.URL,
			ToolPrefix:  server.ToolPrefix,
			Timeout:     server.Timeout,
			AuthHeaders: server.AuthHeaders,
		}, logger)
		if _, err := client.Initialize(ctx); err != nil {
			return assistantExternalMCPRuntime{}, fmt.Errorf("initialize external MCP server %s: %w", server.Name, err)
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			return assistantExternalMCPRuntime{}, fmt.Errorf("list tools for external MCP server %s: %w", server.Name, err)
		}
		for _, tool := range tools {
			if _, exists := bundle.clients[tool.Name]; exists {
				return assistantExternalMCPRuntime{}, fmt.Errorf("external MCP tool %q is registered more than once", tool.Name)
			}
			bundle.clients[tool.Name] = client
			bundle.descriptors = append(bundle.descriptors, mcp.ExternalToolDescriptor{
				ServerName:    server.Name,
				Tool:          mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema},
				Effect:        server.DefaultEffect,
				DefaultRisk:   server.DefaultRisk,
				ResourceTypes: append([]string(nil), server.ResourceTypes...),
				AgentSafe:     true,
			})
		}
		bundle.permissionRules = append(bundle.permissionRules, assistantExternalPermissionRules(server)...)
	}
	return bundle, nil
}

func assistantExternalPermissionRules(server config.AssistantExternalMCPServerConfig) []service.AssistantPermissionRule {
	rules := make([]service.AssistantPermissionRule, 0, len(server.Permissions))
	for i, permission := range server.Permissions {
		id := strings.TrimSpace(permission.ID)
		if id == "" {
			id = fmt.Sprintf("external-mcp-%s-%d", server.Name, i+1)
		}
		rule := service.AssistantPermissionRule{
			ID:             id,
			Decision:       permission.Decision,
			ToolNames:      prefixExternalPermissionToolNames(server.ToolPrefix, permission.ToolNames),
			ToolPrefixes:   prefixExternalPermissionToolPrefixes(server.ToolPrefix, permission.ToolPrefixes),
			Effects:        append([]domain.AssistantToolEffect(nil), permission.Effects...),
			Risks:          append([]domain.AssistantToolRisk(nil), permission.Risks...),
			ExecutionModes: append([]domain.AssistantToolExecutionMode(nil), permission.ExecutionModes...),
			ResourceTypes:  append([]string(nil), permission.ResourceTypes...),
			Reason:         permission.Reason,
		}
		if len(rule.ToolNames) == 0 && len(rule.ToolPrefixes) == 0 {
			rule.ToolPrefixes = []string{server.ToolPrefix}
		}
		rules = append(rules, rule)
	}
	return rules
}

func prefixExternalPermissionToolNames(prefix string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			name = prefix + name
		}
		out = append(out, name)
	}
	return out
}

func prefixExternalPermissionToolPrefixes(prefix string, prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, value := range prefixes {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, prefix) {
			value = prefix + value
		}
		out = append(out, value)
	}
	return out
}

type assistantMCPRuntimeAdapter struct {
	server        *mcp.Server
	externalTools map[string]*mcpclient.Client
}

func (a assistantMCPRuntimeAdapter) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*service.AssistantToolRuntimeToolResult, error) {
	if client, ok := a.externalTools[name]; ok {
		args := make(map[string]any, len(arguments))
		for key, value := range arguments {
			args[key] = value
		}
		result, err := client.CallTool(ctx, name, args)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		content := make([]service.AssistantToolRuntimeToolContent, 0, len(result.Content))
		for _, item := range result.Content {
			content = append(content, service.AssistantToolRuntimeToolContent{Type: item.Type, Text: item.Text})
		}
		return &service.AssistantToolRuntimeToolResult{Content: content, IsError: result.IsError}, nil
	}
	if a.server == nil {
		return nil, fmt.Errorf("assistant MCP server is not configured")
	}
	result, err := a.server.CallTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	content := make([]service.AssistantToolRuntimeToolContent, 0, len(result.Content))
	for _, item := range result.Content {
		content = append(content, service.AssistantToolRuntimeToolContent{Type: item.Type, Text: item.Text})
	}
	return &service.AssistantToolRuntimeToolResult{Content: content, IsError: result.IsError}, nil
}

func (a assistantMCPRuntimeAdapter) InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	if a.server == nil {
		return nil, fmt.Errorf("assistant MCP server is not configured")
	}
	receipt, err := a.server.InvokeAssistantAsyncTool(ctx, name, args)
	if errors.Is(err, mcp.ErrToolCallUnauthorized) {
		return nil, fmt.Errorf("%w: %v", service.ErrAssistantToolCallRefused, err)
	}
	return receipt, err
}

type assistantToolRegistryAdapter struct {
	registry mcp.AssistantToolRegistry
}

func (a assistantToolRegistryAdapter) GetAgentTool(name string) (service.AssistantToolRuntimeToolDescriptor, bool) {
	descriptor, ok := a.registry.GetAgentTool(name)
	if !ok {
		return service.AssistantToolRuntimeToolDescriptor{}, false
	}
	return service.AssistantToolRuntimeToolDescriptor{
		Name:          descriptor.Tool.Name,
		ExecutionMode: descriptor.ExecutionMode,
		Effect:        descriptor.Effect,
		DefaultRisk:   descriptor.DefaultRisk,
		ResourceTypes: append([]string(nil), descriptor.ResourceTypes...),
		InputSchema:   descriptor.Tool.InputSchema,
	}, true
}

type assistantToolSchemaProvider struct {
	registry mcp.AssistantToolRegistry
}

func (p assistantToolSchemaProvider) AgentToolSchemas(context.Context) ([]llmadapter.AgentToolSchema, error) {
	descriptors := p.registry.AgentTools()
	if len(descriptors) == 0 {
		return nil, nil
	}
	schemas := make([]llmadapter.AgentToolSchema, 0, len(descriptors))
	for _, descriptor := range descriptors {
		schemas = append(schemas, llmadapter.AgentToolSchema{
			Name:        descriptor.Tool.Name,
			Description: descriptor.Tool.Description,
			InputSchema: assistantInputSchemaMap(descriptor.Tool.InputSchema),
			Metadata: map[string]any{
				"effect":         string(descriptor.Effect),
				"execution_mode": string(descriptor.ExecutionMode),
				"default_risk":   string(descriptor.DefaultRisk),
				"resource_types": append([]string(nil), descriptor.ResourceTypes...),
			},
		})
	}
	return schemas, nil
}

func assistantInputSchemaMap(schema map[string]interface{}) map[string]any {
	if len(schema) == 0 {
		return nil
	}
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		out[key] = value
	}
	return out
}

func assistantTranscriptKeyProvider(cfg *config.Config) (service.AssistantTranscriptKeyProvider, error) {
	privateKey := ""
	if cfg != nil {
		privateKey = strings.TrimSpace(cfg.Nostr.PrivateKey)
	}
	if privateKey == "" {
		return nil, fmt.Errorf("assistant transcript and checkpoint key requires nostr.private_key when assistant.enabled=true")
	}
	sum := sha256.Sum256([]byte("bahia assistant transcript key v1\x00" + privateKey))
	return service.StaticAssistantTranscriptKeyProvider{Key: service.AssistantTranscriptKey{
		Ref:      "assistant-transcript/service-nostr-key",
		Version:  "v1",
		Rotation: "service-nostr-key",
		Key:      sum[:],
	}}, nil
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

func newLoomCanonicalProjectionSigner(cfg *config.Config, relays []string, logger *zap.Logger) (loom.CanonicalSigner, *signetAdapter.ConnectionManager, error) {
	if cfg == nil || !cfg.Loom.CanonicalProjection.Enabled {
		return nil, nil, nil
	}
	projection := cfg.Loom.CanonicalProjection
	if strings.TrimSpace(projection.SignetBunkerURI) == "" && !cfg.DevMode {
		return nil, nil, fmt.Errorf("loom.canonical_projection.signet_bunker_uri is required")
	}
	signetClient, err := signetAdapter.NewClient(signetAdapter.Config{
		BunkerURI:         projection.SignetBunkerURI,
		Relays:            relays,
		ClientSecretKey:   projection.SignetClientSecretKey,
		RequireReal:       !cfg.DevMode,
		AllowMock:         cfg.DevMode,
		ClosedRetryBudget: cfg.Nostr.ClosedRetryBudget,
	}, slog.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("initialize Signet client: %w", err)
	}
	manager := signetAdapter.NewConnectionManager(signetClient, signetAdapter.ConnectionManagerConfig{
		Name: "loom", AttemptTimeout: projection.SignetConnectTimeout, Logger: slog.Default(),
	})
	logger.Info("Loom canonical projection Signet client configured for asynchronous connection")
	return signetClient, manager, nil
}

func registerSignetHealthCheck(provider *HealthProvider, manager *signetAdapter.ConnectionManager) {
	if provider == nil || manager == nil {
		return
	}
	provider.RegisterCheck(manager.Name(), func() HealthCheck {
		state := manager.State()
		status := HealthStatusWarn
		message := "Signet signer is disconnected; signing-required operations fail with ErrNotConnected"
		if state.Connected {
			status = HealthStatusPass
			message = "Signet signer is connected"
		}
		details := map[string]string{
			"state":        "disconnected",
			"last_error":   state.LastError,
			"last_attempt": "",
			"last_success": "",
		}
		if state.Connected {
			details["state"] = "connected"
		}
		if !state.LastAttempt.IsZero() {
			details["last_attempt"] = state.LastAttempt.Format(time.RFC3339Nano)
		}
		if !state.LastSuccess.IsZero() {
			details["last_success"] = state.LastSuccess.Format(time.RFC3339Nano)
		}
		return HealthCheck{Name: manager.Name(), Status: status, Message: message, Details: details}
	})
}

type operatorAssistantBootstrapRunner struct {
	signer  *signetAdapter.Client
	reactor *soulfactory.Reactor
	logger  *zap.Logger
}

func (r *operatorAssistantBootstrapRunner) Name() string { return "operator-assistant-soul-bootstrap" }

func (r *operatorAssistantBootstrapRunner) Run(ctx context.Context) error {
	for {
		if err := r.signer.WaitUntilConnected(ctx); err != nil {
			return nil
		}
		if _, err := soulfactory.EnsureOperatorAssistantSoul(ctx, r.reactor); err != nil && ctx.Err() == nil {
			r.logger.Warn("operator assistant soul bootstrap failed", zap.Error(err))
		}
		if err := r.signer.WaitUntilDisconnected(ctx); err != nil {
			return nil
		}
	}
}

func bootstrapOperatorAssistant(cfg *config.Config, relays []string, logger *zap.Logger) (service.AssistantIdentity, *signetAdapter.ConnectionManager, BackgroundRunner, service.ConfigFabricSigner) {
	identity := service.AssistantIdentity{AgentID: soulfactory.OperatorAssistantAgentID}
	if cfg != nil && strings.TrimSpace(cfg.Nostr.PrivateKey) != "" {
		if secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey)); err == nil {
			identity.Pubkey = secret.Public().Hex()
		}
	}
	if cfg == nil || (!cfg.DevMode && !cfg.Assistant.SignetAllowMock && strings.TrimSpace(cfg.Assistant.SignetBunkerURI) == "") {
		return identity, nil, nil, nil
	}
	slogLogger := slog.Default()
	signetClient, err := signetAdapter.NewClient(signetAdapter.Config{BunkerURI: cfg.Assistant.SignetBunkerURI, Relays: relays, RequireReal: !cfg.DevMode && !cfg.Assistant.SignetAllowMock, AllowMock: cfg.DevMode || cfg.Assistant.SignetAllowMock, ClosedRetryBudget: cfg.Nostr.ClosedRetryBudget}, slogLogger)
	if err != nil {
		logger.Warn("operator assistant signet client initialization failed; using service-key attribution fallback", zap.Error(err))
		return identity, nil, nil, nil
	}
	soulReactor := soulfactory.NewReactor(soulfactory.Config{Relays: relays, AuthorizedPubkeys: cfg.Nostr.AuthorizedPubkeys}, nil, signetClient, slogLogger)
	manager := signetAdapter.NewConnectionManager(signetClient, signetAdapter.ConnectionManagerConfig{
		Name: "operator-assistant", AttemptTimeout: cfg.Assistant.SignetConnectTimeout, Logger: slogLogger,
	})
	return identity, manager, &operatorAssistantBootstrapRunner{signer: signetClient, reactor: soulReactor, logger: logger}, signetClient
}

const assistantSessionStartupTimeout = 5 * time.Second

func loadAssistantSessions(ctx context.Context, repo repository.NostrEventRepository, logger *zap.Logger) []domain.AssistantSession {
	return loadAssistantSessionsWithTimeout(ctx, repo, logger, assistantSessionStartupTimeout)
}

func loadAssistantSessionsWithTimeout(ctx context.Context, repo repository.NostrEventRepository, logger *zap.Logger, timeout time.Duration) []domain.AssistantSession {
	if repo == nil {
		return nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	records, err := repo.ListByKind(loadCtx, domain.KindAssistantSessionState, 500)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to load assistant session read models", zap.Error(err))
		}
		return nil
	}
	seen := map[string]struct{}{}
	sessions := []domain.AssistantSession{}
	for _, record := range records {
		// Only historical v1 sessions seed the read-only legacy cache; v2
		// projections are hydrated by the executor's recovery path.
		if assistantRecordSchema(record.Tags) != domain.AssistantSessionSchema {
			continue
		}
		var session domain.AssistantSession
		if err := json.Unmarshal([]byte(record.Content), &session); err != nil {
			if logger != nil {
				logger.Warn("failed to parse assistant session read model", zap.String("event_id", record.ID), zap.Error(err))
			}
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

func assistantRecordSchema(raw json.RawMessage) string {
	var tags [][]string
	if len(raw) == 0 || json.Unmarshal(raw, &tags) != nil {
		return ""
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == domain.AssistantSessionTagSchema {
			return tag[1]
		}
	}
	return ""
}

// controlplaneRunner adapts the controlplane.Reactor to the BackgroundRunner interface.
type controlplaneRunner struct {
	reactor *controlplane.Reactor
}

func (r *controlplaneRunner) Name() string { return "controlplane" }
func (r *controlplaneRunner) Run(ctx context.Context) error {
	return r.reactor.Run(ctx)
}

func buildRelayAdminClient(ctx context.Context, cfg *config.Config, secretRepo repository.SecretRepository, secretEncryptor *secretsAdapter.Encryptor, logger *zap.Logger) controlplane.RelayAdminCaller {
	if cfg == nil || !cfg.Nostr.RelayAdministration.Enabled {
		return nil
	}
	resolver := secretsAdapter.NewResolver(secretRepo, secretEncryptor)
	privateKey, err := resolver.ResolveSecret(ctx, cfg.Nostr.RelayAdministration.AdministratorPrivateKeyRef)
	if err != nil {
		logger.Warn("nip-86 relay administration disabled because administrator private key could not be resolved", zap.Error(err))
		return nil
	}
	targets := make([]relayadmin.Target, 0, len(cfg.Nostr.RelayAdministration.Targets))
	for _, target := range cfg.Nostr.RelayAdministration.Targets {
		targets = append(targets, relayadmin.Target{
			Ref:                  target.Ref,
			RelayURL:             target.RelayURL,
			HTTPURL:              target.HTTPURL,
			AdministratorPubkeys: target.AdministratorPubkeys,
		})
	}
	client, err := relayadmin.NewClient(relayadmin.Config{
		Enabled:       true,
		PrivateKeyHex: strings.TrimSpace(privateKey),
		Targets:       targets,
		HTTPClient:    &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		logger.Warn("nip-86 relay administration disabled because client validation failed", zap.Error(err))
		return nil
	}
	return client
}

type encryptedRequestTransportRunner struct {
	transport *controlplane.EncryptedRequestTransport
}

func (r *encryptedRequestTransportRunner) Name() string { return "encrypted-request-result-events" }
func (r *encryptedRequestTransportRunner) Run(ctx context.Context) error {
	return r.transport.Run(ctx)
}

type securityRelaySubscriber struct {
	pool *nostrAdapter.RelayPool
}

func (s securityRelaySubscriber) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (service.SecuritySubscription, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("security relay pool is not configured")
	}
	merged, err := s.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return securityMergedSubscription{merged: merged}, nil
}

func (s securityRelaySubscriber) AuthenticateRelay(ctx context.Context, relayURL string) error {
	if s.pool == nil {
		return fmt.Errorf("security relay pool is not configured")
	}
	return s.pool.AuthenticateRelay(ctx, relayURL)
}

type securityMergedSubscription struct {
	merged *nostrAdapter.MergedSubscription
}

func (s securityMergedSubscription) Close() {
	if s.merged != nil {
		s.merged.Close()
	}
}

func (s securityMergedSubscription) Next(ctx context.Context) (service.SecuritySubscriptionMessage, bool, error) {
	if s.merged == nil {
		return service.SecuritySubscriptionMessage{}, false, nil
	}
	for s.merged.Events != nil || s.merged.EndOfStoredEvents != nil || s.merged.RelayEOSE != nil || s.merged.Closed != nil {
		select {
		case <-ctx.Done():
			return service.SecuritySubscriptionMessage{}, false, ctx.Err()
		case ev, ok := <-s.merged.Events:
			if ok {
				return service.SecuritySubscriptionMessage{Event: ev}, true, nil
			}
			s.merged.Events = nil
		case _, ok := <-s.merged.EndOfStoredEvents:
			if ok {
				return service.SecuritySubscriptionMessage{EOSE: true}, true, nil
			}
			s.merged.EndOfStoredEvents = nil
		case eose, ok := <-s.merged.RelayEOSE:
			if ok {
				return service.SecuritySubscriptionMessage{RelayEOSE: service.SecurityRelayEOSE{RelayURL: eose.RelayURL, SubscriptionID: eose.SubscriptionID}}, true, nil
			}
			s.merged.RelayEOSE = nil
		case closed, ok := <-s.merged.Closed:
			if ok {
				return service.SecuritySubscriptionMessage{Closed: service.SecurityRelayClosed{RelayURL: closed.RelayURL, SubscriptionID: closed.SubscriptionID, Reason: closed.Reason}}, true, nil
			}
			s.merged.Closed = nil
		}
	}
	return service.SecuritySubscriptionMessage{}, false, nil
}

type sbomAvailabilityRelaySubscriber struct {
	pool *nostrAdapter.RelayPool
}

func (s sbomAvailabilityRelaySubscriber) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (service.SBOMAvailabilitySubscription, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("SBOM availability relay pool is not configured")
	}
	merged, err := s.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return sbomAvailabilityMergedSubscription{merged: merged}, nil
}

func (s sbomAvailabilityRelaySubscriber) AuthenticateRelay(ctx context.Context, relayURL string) error {
	if s.pool == nil {
		return fmt.Errorf("SBOM availability relay pool is not configured")
	}
	return s.pool.AuthenticateRelay(ctx, relayURL)
}

type sbomAvailabilityMergedSubscription struct {
	merged *nostrAdapter.MergedSubscription
}

func (s sbomAvailabilityMergedSubscription) Close() {
	if s.merged != nil {
		s.merged.Close()
	}
}

func (s sbomAvailabilityMergedSubscription) Next(ctx context.Context) (service.SBOMAvailabilitySubscriptionMessage, bool, error) {
	if s.merged == nil {
		return service.SBOMAvailabilitySubscriptionMessage{}, false, nil
	}
	for s.merged.Events != nil || s.merged.EndOfStoredEvents != nil || s.merged.RelayEOSE != nil || s.merged.Closed != nil {
		select {
		case <-ctx.Done():
			return service.SBOMAvailabilitySubscriptionMessage{}, false, ctx.Err()
		case ev, ok := <-s.merged.Events:
			if ok {
				return service.SBOMAvailabilitySubscriptionMessage{Event: ev}, true, nil
			}
			s.merged.Events = nil
		case _, ok := <-s.merged.EndOfStoredEvents:
			if ok {
				return service.SBOMAvailabilitySubscriptionMessage{EOSE: true}, true, nil
			}
			s.merged.EndOfStoredEvents = nil
		case eose, ok := <-s.merged.RelayEOSE:
			if ok {
				return service.SBOMAvailabilitySubscriptionMessage{RelayEOSE: service.SBOMAvailabilityRelayEOSE{RelayURL: eose.RelayURL, SubscriptionID: eose.SubscriptionID}}, true, nil
			}
			s.merged.RelayEOSE = nil
		case closed, ok := <-s.merged.Closed:
			if ok {
				return service.SBOMAvailabilitySubscriptionMessage{Closed: service.SBOMAvailabilityRelayClosed{RelayURL: closed.RelayURL, SubscriptionID: closed.SubscriptionID, Reason: closed.Reason}}, true, nil
			}
			s.merged.Closed = nil
		}
	}
	return service.SBOMAvailabilitySubscriptionMessage{}, false, nil
}

type sbomPublishAdapter struct {
	publisher *nostrAdapter.Publisher
}

// PublishSignedEventWithResults keeps the per-relay results alongside the
// error: an ErrPublishIncomplete publish is queued, not lost, an
// ErrPublishAbandoned one is terminal, and callers inspect both.
// DeliveryOutcome reports what the publisher's outbox knows about eventID.
func (a sbomPublishAdapter) DeliveryOutcome(ctx context.Context, eventID string) (nostrutil.DeliveryOutcome, error) {
	if a.publisher == nil {
		return nostrutil.DeliveryUnknown, nil
	}
	return a.publisher.DeliveryOutcome(ctx, eventID)
}

func (a sbomPublishAdapter) PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]sbomAdapter.PublishOKResult, error) {
	if a.publisher == nil {
		return nil, fmt.Errorf("nostr publisher is not configured")
	}
	results, err := a.publisher.PublishSignedEventWithResults(ctx, ev)
	out := make([]sbomAdapter.PublishOKResult, 0, len(results))
	for _, result := range results {
		out = append(out, sbomAdapter.PublishOKResult{RelayURL: result.RelayURL, Accepted: result.Accepted, Reason: result.Reason, Error: result.Error})
	}
	return out, err
}

// configFabricPublishAdapter delivers operator-signed config-fabric events
// through the control-plane outbox publisher.
type configFabricPublishAdapter struct {
	publisher *nostrAdapter.Publisher
}

func (a configFabricPublishAdapter) PublishPresignedEvent(ctx context.Context, ev nostr.Event, entityType string) error {
	if a.publisher == nil {
		return fmt.Errorf("config-fabric relay publisher is not configured")
	}
	if a.publisher.Target() != repository.NostrPublishTargetControlPlane {
		// The service records its rows for the control-plane runner.
		return fmt.Errorf("config-fabric publisher must target %q, got %q", repository.NostrPublishTargetControlPlane, a.publisher.Target())
	}
	_, err := a.publisher.PublishPresignedEvent(ctx, ev, entityType)
	return err
}

// newDocsRelayQuerier creates a NostrDocsQuerier that queries existing NIP-23
// documentation events from the relay pool.
func newDocsRelayQuerier(pool *nostrAdapter.RelayPool, pubkey string, logger *zap.Logger) docs.NostrDocsQuerier {
	return docs.DocsQuerierFunc(func(ctx context.Context, _ string) ([]*nostr.Event, error) {
		if pool == nil {
			return nil, fmt.Errorf("relay pool not configured")
		}

		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		parsedPubkey, err := nostr.PubKeyFromHex(pubkey)
		if err != nil {
			return nil, fmt.Errorf("parse docs author pubkey: %w", err)
		}
		filter := nostr.Filter{
			Kinds:   []nostr.Kind{nostr.Kind(kinds.LongFormContent)},
			Authors: []nostr.PubKey{parsedPubkey},
			Tags: nostr.TagMap{
				"t": []string{"bahia-docs"},
			},
		}

		merged, err := pool.SubscribeAllWithEOSE(queryCtx, []nostr.Filter{filter})
		if err != nil {
			return nil, fmt.Errorf("subscribing for existing doc events: %w", err)
		}
		defer merged.Close()

		var events []*nostr.Event
		for {
			select {
			case <-queryCtx.Done():
				return events, nil
			case <-merged.EndOfStoredEvents:
				return events, nil
			case ev, ok := <-merged.Events:
				if !ok {
					return events, nil
				}
				events = append(events, ev)
			}
		}
	})
}

// buildIntentAuthorsSyncer creates an IntentAuthorsSyncer that pushes the
// TrustSet's principal set to Bahia-owned sidecar relays via NIP-86. Returns
// nil if relay administration is disabled, has no Bahia-owned targets, or the
// admin private key cannot be resolved. In those cases the sidecar's intent
// authors set starts empty, and only admin-allowlisted pubkeys can publish.
func buildIntentAuthorsSyncer(ctx context.Context, cfg *config.Config, trustSet *controlplane.TrustSet, secretRepo repository.SecretRepository, secretEncryptor *secretsAdapter.Encryptor, logger *zap.Logger) *controlplane.IntentAuthorsSyncer {
	if cfg == nil || !cfg.Nostr.RelayAdministration.Enabled {
		return nil
	}

	// Filter to Bahia-owned targets only.
	var bahiaOwnedTargets []relayadmin.Target
	for _, target := range cfg.Nostr.RelayAdministration.Targets {
		if target.Authorization != config.RelayAdministrationBahiaOwned {
			continue
		}
		bahiaOwnedTargets = append(bahiaOwnedTargets, relayadmin.Target{
			Ref:                  target.Ref,
			RelayURL:             target.RelayURL,
			HTTPURL:              target.HTTPURL,
			AdministratorPubkeys: target.AdministratorPubkeys,
		})
	}
	if len(bahiaOwnedTargets) == 0 {
		logger.Info("intent authors syncer disabled: no bahia_owned relay admin targets configured")
		return nil
	}

	resolver := secretsAdapter.NewResolver(secretRepo, secretEncryptor)
	privateKey, err := resolver.ResolveSecret(ctx, cfg.Nostr.RelayAdministration.AdministratorPrivateKeyRef)
	if err != nil {
		logger.Warn("intent authors syncer disabled: administrator private key could not be resolved", zap.Error(err))
		return nil
	}

	client, err := relayadmin.NewClient(relayadmin.Config{
		Enabled:       true,
		PrivateKeyHex: strings.TrimSpace(privateKey),
		Targets:       bahiaOwnedTargets,
		HTTPClient:    &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		logger.Warn("intent authors syncer disabled: relay admin client validation failed", zap.Error(err))
		return nil
	}

	targetRefs := make([]string, 0, len(bahiaOwnedTargets))
	for _, t := range bahiaOwnedTargets {
		targetRefs = append(targetRefs, t.Ref)
	}

	return controlplane.NewIntentAuthorsSyncer(controlplane.IntentAuthorsSyncerConfig{
		TrustSet:   trustSet,
		Admin:      client,
		TargetRefs: targetRefs,
		Logger:     logger,
	})
}
